#!/bin/sh
# Run directly in the terminal being tested, not through an output filter.
set -eu

trap 'printf "\033[0m"' EXIT

printf 'Compare each pair. ANSI faint is SGR 2; the last pair uses explicit RGB colors.\n\n'
printf 'Default:       \033[0mNormal text     \033[2mFaint text\033[0m\n'
printf 'Agent color:   \033[38;5;170mreviewer        \033[2mreviewer\033[0m\n'
printf 'Bold agent:    \033[1;38;5;170mreviewer        \033[2mreviewer\033[0m\n'
printf 'RGB agent:     \033[38;2;215;95;215mreviewer        \033[2mreviewer\033[0m\n'
printf 'RGB fallback:  \033[38;2;215;95;215mreviewer        \033[38;2;140;70;140mreviewer\033[0m\n'
printf '\nIf only the last pair differs, use an explicit muted color instead of ANSI faint.\n'

# Same fixed palette pairs used for muted agent identities in Mekugi.
printf '\nMekugi agent palette (normal -> muted):\n'
for pair in 39:31 170:133 38:30 99:61 37:29 133:96 74:67; do
    normal=${pair%:*}
    muted=${pair#*:}
    printf '%3s -> %3s:  \033[38;5;%smreviewer  \033[38;5;%smreviewer\033[0m\n' "$normal" "$muted" "$normal" "$muted"
done
printf 'Shared dim text: \033[38;5;243mmetadata, separators, status text\033[0m\n'
