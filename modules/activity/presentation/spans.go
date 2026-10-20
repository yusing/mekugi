package presentation

// TextSpan identifies a bound, atomic token, not text resembling a placeholder.
// Offsets are UTF-8 byte boundaries; spans must not overlap.
type TokenKind uint8

const (
	ImageToken TokenKind = iota
	FileToken
	SkillToken
	SelectionToken
)

type TextSpan struct {
	Start, End int
	Kind       TokenKind
}
