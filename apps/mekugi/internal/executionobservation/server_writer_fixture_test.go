package executionobservation

type serverErrorWriter struct{ err error }

func (w serverErrorWriter) Write([]byte) (int, error) { return 0, w.err }
