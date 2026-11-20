import io
import subprocess
import zipfile

import pytest
from pypdf import PdfWriter
from pypdf.generic import DecodedStreamObject, DictionaryObject, NameObject

from faq_ai.parsers import Section, chunks_for, parse_bytes, parse_isolated, ParseError


def docx(body, **kwargs):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("word/document.xml", '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>' + body + '</w:body></w:document>')
        for name, data in kwargs.items():
            archive.writestr(name, data)
    return stream.getvalue()


def pdf(text=None, encrypted=False):
    writer = PdfWriter()
    page = writer.add_blank_page(width=200, height=200)
    if text:
        font = DictionaryObject({NameObject("/Type"): NameObject("/Font"), NameObject("/Subtype"): NameObject("/Type1"), NameObject("/BaseFont"): NameObject("/Helvetica")})
        page[NameObject("/Resources")] = DictionaryObject({NameObject("/Font"): DictionaryObject({NameObject("/F1"): writer._add_object(font)})})
        content = DecodedStreamObject()
        content.set_data(f"BT /F1 12 Tf 10 100 Td ({text}) Tj ET".encode())
        page[NameObject("/Contents")] = writer._add_object(content)
    if encrypted:
        writer.encrypt("secret")
    out = io.BytesIO()
    writer.write(out)
    return out.getvalue()


def test_russian_markdown_and_manual_text_keep_headings_and_offsets():
    raw = '# Условия\r\n\r\nДоставка стоит 500 рублей.\r\n\r\nСамовывоз доступен.'.encode('utf-8')
    sections = parse_bytes(raw, 'md')
    assert sections[1].heading == 'Условия'
    normalized = raw.decode().replace('\r\n', '\n')
    assert normalized[sections[1].offset:].startswith(sections[1].text)
    assert parse_bytes('Ручной текст на русском'.encode(), 'text')[0].text == 'Ручной текст на русском'


@pytest.mark.parametrize('kind,raw,code', [
    ('txt', b'', 'empty_document'), ('text', b' \r\n ', 'empty_document'),
    ('txt', b'\xff', 'invalid_encoding'), ('txt', b'\x00bad', 'invalid_document'),
    ('pdf', b'not a pdf', 'corrupt_pdf'), ('pdf', pdf(), 'scanned_or_empty_pdf'),
    ('pdf', pdf(encrypted=True), 'encrypted_pdf'), ('docx', b'bad zip', 'corrupt_docx'),
    ('exe', b'anything', 'unsupported_format'),
])
def test_typed_parse_failures(kind, raw, code):
    with pytest.raises(ParseError) as error:
        parse_bytes(raw, kind)
    assert error.value.code == code


def test_pdf_page_provenance():
    sections = parse_bytes(pdf('Delivery costs 500 rubles.'), 'pdf')
    assert sections[0].page == 1 and '500' in sections[0].text


def test_docx_tables_headings_and_no_external_relationships():
    raw = docx('<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Условия</w:t></w:r></w:p>'
               '<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Доставка 500 рублей</w:t></w:r></w:p></w:tc></w:tr></w:tbl>',
               **{'word/_rels/document.xml.rels': '<Relationships><Relationship Target="file:///secret"/></Relationships>'})
    sections = parse_bytes(raw, 'docx')
    assert sections[1].text == 'Доставка 500 рублей'
    assert sections[1].heading == 'Условия'


def test_docx_zip_bomb_and_entity_expansion_are_rejected():
    with pytest.raises(ParseError) as error:
        parse_bytes(docx('<w:p/>', bomb='A' * 100000), 'docx')
    assert error.value.code == 'unsafe_docx_archive'
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, 'w') as archive:
        archive.writestr('word/document.xml', '<!DOCTYPE x [<!ENTITY z SYSTEM "file:///secret">]><x>&z;</x>')
    with pytest.raises(ParseError) as error:
        parse_bytes(stream.getvalue(), 'docx')
    assert error.value.code == 'corrupt_docx'


def test_chunk_ids_stable_versioned_and_real_offsets():
    text = 'Доставка в Москву стоит 500 рублей. ' * 20
    sections = [Section(text, page=3, heading='Доставка', offset=17)]
    first = chunks_for(sections, 'document', 1, 160, 30)
    assert first == chunks_for(sections, 'document', 1, 160, 30)
    assert {c['chunk_id'] for c in first}.isdisjoint({c['chunk_id'] for c in chunks_for(sections, 'document', 2, 160, 30)})
    for chunk in first:
        assert text[chunk['start_char']-17:].startswith(chunk['content'])
        assert len(chunk['content']) <= 160 and chunk['page'] == 3


def test_parser_process_timeout_is_typed(monkeypatch):
    def stalled(*args, **kwargs):
        raise subprocess.TimeoutExpired('parser', 1)
    monkeypatch.setattr(subprocess, 'run', stalled)
    with pytest.raises(ParseError) as error:
        parse_isolated(b'file', 'pdf', 1)
    assert error.value.code == 'parse_timeout'


def test_real_parser_subprocess_handles_russian():
    assert parse_isolated('Доставка 500 рублей'.encode(), 'txt')[0].text == 'Доставка 500 рублей'
