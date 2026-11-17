package composer

// UploadAttachment retains supplied text in its draft binding, never a host path.
func UploadAttachment(name string, data []byte) Token {
	return Token{Name: name, Source: "upload", Text: string(data)}
}
