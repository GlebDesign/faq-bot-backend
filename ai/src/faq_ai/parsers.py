"""Bounded, isolated parsers. No URLs, macros or external relationships are opened."""
import hashlib
import io
import json
import re
import subprocess
import sys
import zipfile
from dataclasses import asdict, dataclass
from pathlib import Path

from defusedxml import ElementTree
from langchain_text_splitters import RecursiveCharacterTextSplitter

from faq_ai.jobs import JobError


@dataclass
class Section:
    text: str
    page: int = 0
    heading: str = ""
    offset: int = 0


def normalize(text):
    return text.replace("\r\n", "\n").replace("\r", "\n").replace("\u00a0", " ")


def parse_bytes(raw: bytes, kind: str) -> list[Section]:
    if not raw or len(raw) > 10_000_000:
        raise JobError("file_size_limit" if raw else "empty_document")
    if kind in {"txt", "md", "text"}:
        try:
            text = normalize(raw.decode("utf-8-sig"))
        except UnicodeDecodeError as exc:
            raise JobError("invalid_encoding") from exc
        if "\x00" in text:
            raise JobError("invalid_document")
        sections, heading, offset = [], "", 0
        for paragraph in re.split(r"(\n\s*\n)", text):
            if paragraph.strip():
                first = paragraph.splitlines()[0]
                if kind == "md" and first.startswith("#"):
                    heading = first.lstrip("# ")[:200]
                sections.append(Section(paragraph, heading=heading, offset=offset))
            offset += len(paragraph)
    elif kind == "pdf":
        from pypdf import PdfReader, apply_configuration
        from pypdf.errors import LimitReachedError
        try:
            with apply_configuration(maximum_declared_stream_length=10_000_000,
                    array_based_stream_maximum_output_length=10_000_000,
                    zlib_maximum_output_length=10_000_000, lzw_maximum_output_length=10_000_000,
                    run_length_maximum_output_length=10_000_000, image_maximum_buffer_size=10_000_000,
                    stream_decoding_work_maximum_length=20_000_000, jbig2dec_binary=None):
                reader = PdfReader(io.BytesIO(raw), strict=True)
                if reader.is_encrypted:
                    raise JobError("encrypted_pdf")
                if len(reader.pages) > 200:
                    raise JobError("page_limit")
                sections = []
                for i, page in enumerate(reader.pages, 1):
                    # Bound decompressed page streams before extract_text allocates its parse tree.
                    stream = page.get_contents()
                    if stream and len(stream.get_data()) > 10_000_000:
                        raise JobError("pdf_stream_limit")
                    text = normalize(page.extract_text() or "")
                    if text.strip():
                        sections.append(Section(text, page=i))
                    if sum(len(s.text) for s in sections) > 2_000_000:
                        raise JobError("text_limit")
        except JobError:
            raise
        except LimitReachedError as exc:
            raise JobError("pdf_resource_limit") from exc
        except Exception as exc:
            raise JobError("corrupt_pdf") from exc
        if not sections:
            raise JobError("scanned_or_empty_pdf")
    elif kind == "docx":
        try:
            with zipfile.ZipFile(io.BytesIO(raw)) as archive:
                entries = archive.infolist()
                if (len(entries) > 1000 or sum(e.file_size for e in entries) > 25_000_000
                        or any(e.flag_bits & 1 or e.file_size > 10_000_000
                               or e.file_size > max(e.compress_size, 1) * 100 for e in entries)):
                    raise JobError("unsafe_docx_archive")
                if "word/vbaProject.bin" in archive.namelist():
                    raise JobError("unsupported_macros")
                body = ElementTree.fromstring(archive.read("word/document.xml"))
            ns = {"w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main"}
            sections, heading, offset = [], "", 0
            # Document-order paragraphs include table cells, without extracting files.
            for paragraph in body.findall(".//w:p", ns):
                text = normalize("".join(n.text or "" for n in paragraph.findall(".//w:t", ns)))
                style = paragraph.find("w:pPr/w:pStyle", ns)
                if style is not None and "heading" in str(style.attrib).lower():
                    heading = text[:200]
                if text.strip():
                    sections.append(Section(text, heading=heading, offset=offset))
                    offset += len(text) + 1
        except JobError:
            raise
        except Exception as exc:
            raise JobError("corrupt_docx") from exc
    else:
        raise JobError("unsupported_format")
    sections = [s for s in sections if s.text.strip()]
    if not sections:
        raise JobError("empty_document")
    if sum(len(s.text) for s in sections) > 2_000_000:
        raise JobError("text_limit")
    return sections


def parse_isolated(raw, kind, timeout=20):
    try:
        process = subprocess.run([sys.executable, "-m", "faq_ai.parsers", kind], input=raw,
                                 stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout)
    except OSError as exc:
        raise JobError("parser_unavailable", transient=True) from exc
    except subprocess.TimeoutExpired as exc:
        raise JobError("parse_timeout") from exc
    try:
        result = json.loads(process.stdout)
        if process.returncode or "error" in result:
            raise JobError(result.get("error", "parse_failed"))
        return [Section(**s) for s in result["sections"]]
    except (ValueError, KeyError, TypeError) as exc:
        raise JobError("parse_failed") from exc


def chunks_for(sections, document_id, version, chunk_size=800, overlap=120):
    splitter = RecursiveCharacterTextSplitter(chunk_size=chunk_size, chunk_overlap=overlap, add_start_index=True)
    result = []
    for section in sections:
        for part in splitter.create_documents([section.text]):
            number = len(result)
            content = part.page_content
            key = hashlib.sha256(f"{document_id}:{version}:{number}:{content}".encode()).hexdigest()
            result.append({"chunk_id": key, "number": number, "content": content,
                           "page": section.page, "heading": section.heading,
                           "start_char": section.offset + part.metadata["start_index"]})
            if len(result) > 1000:
                raise JobError("chunk_limit")
    if not result:
        raise JobError("empty_document")
    return result


def read_source(root: Path, key: str, expected_hash: str, max_bytes=10_000_000):
    # Metadata key is resolved under an allowlisted, read-only mount; never a URL.
    if not key or "\\" in key or ":" in key or Path(key).is_absolute() or ".." in Path(key).parts:
        raise JobError("invalid_storage_key")
    path = (root.resolve() / key).resolve()
    if not path.is_relative_to(root.resolve()):
        raise JobError("invalid_storage_key")
    try:
        with path.open("rb") as file:
            raw = file.read(max_bytes + 1)
    except OSError as exc:
        raise JobError("source_unavailable", transient=True) from exc
    if len(raw) > max_bytes:
        raise JobError("file_size_limit")
    if hashlib.sha256(raw).hexdigest() != expected_hash:
        raise JobError("source_hash_mismatch")
    return raw


if __name__ == "__main__":
    # POSIX hard ceiling also bounds pathological PDF decompression. Windows uses
    # byte/page/text limits plus a killed subprocess deadline.
    if sys.platform != "win32":
        import resource
        resource.setrlimit(resource.RLIMIT_AS, (512 * 1024 * 1024, 512 * 1024 * 1024))
    try:
        data = sys.stdin.buffer.read(10_000_001)
        sections = parse_bytes(data, sys.argv[1])
        sys.stdout.buffer.write(json.dumps({"sections": [asdict(s) for s in sections]}, ensure_ascii=False).encode())
    except JobError as error:
        sys.stdout.buffer.write(json.dumps({"error": error.code}).encode())
        sys.exit(2)
