// Package codec registers the pinned upstream generated regex engines.
//
// Source: github.com/tiktoken-go/tokenizer/codec/regexp.gen.go@v0.8.1
// SHA-256: e5fabacdfc04a1eb96b6bc390562cf6e301c89a9bf42fc620ea09ffd5ee71ae8
// The generated file is an unmodified upstream snapshot, covered by ../LICENSE.
// Keep the generated engine, not just its pattern: the interpreted regexp2
// engine differs on control characters (including DEL). Keeping the upstream
// registration unit also avoids silently changing process-wide regex behavior.
package codec
