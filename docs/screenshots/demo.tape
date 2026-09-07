# Charm VHS recording of argo9s --demo (no cluster / no real names).
# Render: PATH="$(go env GOPATH)/bin:$PATH" vhs docs/screenshots/demo.tape
#
# VHS uses ttyd/xterm.js — not iTerm. Without COLORTERM, lipgloss may drop
# to ASCII (no colors). Force truecolor here and in --demo.

Output docs/screenshots/demo.gif

Set Shell zsh
Set FontSize 14
Set Width 1200
Set Height 720
Set Padding 8
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set Framerate 20

Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env CLICOLOR_FORCE "1"

Hide
Type "cd /Users/voronov2/github/argo9s && ./bin/argo9s --demo"
Enter
Sleep 2s
Show
Sleep 1s

# Project filter
Type ":"
Sleep 200ms
Type "p staging"
Enter
Sleep 1500ms
Screenshot docs/screenshots/list.png

# Clear filter, open graph
Type ":"
Sleep 200ms
Type "project clear"
Enter
Sleep 800ms
Type "j"
Sleep 200ms
Type "g"
Sleep 2s
Screenshot docs/screenshots/graph.png
Sleep 1s

Type "Q"
Sleep 500ms
