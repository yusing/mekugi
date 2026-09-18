# /// script
# requires-python = ">=3.10"
# dependencies = ["llguidance==1.8.0"]
# ///
"""Check model-side end-of-call reachability with the constrained-output lexer.

Run: uv run contrib/codex/check_tool_grammars.py
This uses a byte vocabulary, not a model or provider request. Engine tests own
delimiter equality, command validation, and the no-effects-on-invalid-input rule.
"""

from pathlib import Path
import unittest

from llguidance import LLMatcher, LLTokenizer, TokenizerWrapper


class ByteVocabulary:
    eos_token_id = 256
    bos_token_id = None
    tokens = [bytes([value]) for value in range(256)] + [b"<eos>"]
    special_token_ids = [256]

    def __call__(self, text):
        return list(text if isinstance(text, bytes) else text.encode("utf-8"))


class ToolGrammarCompletion(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = Path(__file__).resolve().parents[2]
        cls.grammars = {
            "hpatch": (root / "tool_grammar.lark").read_text(),
            "hpatch_recover": (
                root / "internal/router/mekugi_recovery_grammar.lark"
            ).read_text(),
        }
        cls.tokenizer = LLTokenizer(TokenizerWrapper(ByteVocabulary()))

    def assert_completes(self, name, script):
        matcher = LLMatcher(self.tokenizer, self.grammars[name])
        self.assertFalse(matcher.is_error(), matcher.get_error())
        tokens = list(script.encode("utf-8"))
        self.assertEqual(matcher.try_consume_tokens(tokens), len(tokens))
        self.assertTrue(matcher.is_accepting(), repr(script))
        self.assertTrue(matcher.consume_token(ByteVocabulary.eos_token_id))
        self.assertFalse(matcher.is_error(), matcher.get_error())

    def test_heredoc_completion(self):
        for name, headers in {
            "hpatch": ['new a\ntype', 'in a\ntype "old"', 'in a\nadd EOF', 'shell'],
            "hpatch_recover": ['cedar1 value', 'type "old"', 'add EOF'],
        }.items():
            for header in headers:
                for marker, end in [('END', 'END'), ("'END HERE'", 'END HERE'), ('-END', '\tEND')]:
                    for body in ['', '\n', 'text\n', 'one\rtwo\n', 'type "x" "y"\n', '界\n']:
                        for newline in ['\n', '\r\n']:
                            for tail in ['', newline, newline * 2, newline + '  ' + newline]:
                                script = (header + ' <<' + marker + '\n' + body + end).replace('\n', newline) + tail
                                with self.subTest(name=name, script=script):
                                    self.assert_completes(name, script)

    def test_commands_after_heredoc(self):
        for name, script in {
            "hpatch": 'new a\ntype <<END\nfirst\nEND\nnew b\ntype <<NEXT\nsecond\nNEXT\nshell true',
            "hpatch_recover": 'cedar1 value <<END\nfirst\nEND\nbirch2 value <<NEXT\nsecond\nNEXT',
        }.items():
            for tail in ['', '\n', '\n\n']:
                with self.subTest(name=name, tail=tail):
                    self.assert_completes(name, script + tail)

    def test_inline_completion(self):
        for name, script in {
            "hpatch": 'in a\ntype "old" "new"',
            "hpatch_recover": 'cedar1 value "new"',
        }.items():
            for tail in ['', '\n', '\n\n']:
                with self.subTest(name=name, tail=tail):
                    self.assert_completes(name, script + tail)


if __name__ == "__main__":
    unittest.main()
