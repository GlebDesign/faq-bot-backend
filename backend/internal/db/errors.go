package db

func WrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	return &databaseError{message: message, cause: err}
}

type databaseError struct {
	message string
	cause   error
}

func (e *databaseError) Error() string { return e.message }
func (e *databaseError) Unwrap() error { return e.cause }
