# README animated tour of argo9s --demo (offline sample data, no cluster).
# Render: make screenshots   (needs vhs + ttyd: https://github.com/charmbracelet/vhs)

Output docs/screenshots/demo.gif

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1280
Set Height 720
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 90ms
Set Framerate 20
Set PlaybackSpeed 1.0

Env CLICOLOR_FORCE "1"
Env COLORTERM "truecolor"

Hide
Type "./bin/argo9s --demo" Enter
Sleep 2s
Show
Sleep 1.5s

# Live filter
Type "/guest"
Sleep 1.2s
Escape
Sleep 600ms

# ApplicationSets → the apps it generated
Type ":appsets" Enter
Sleep 1.2s
Enter
Sleep 1.5s
Escape
Sleep 600ms

# Clusters
Type ":clusters" Enter
Sleep 1.5s
Type ":apps" Enter
Sleep 800ms

# Managed resources tree
Enter
Sleep 1s
Down 2
Sleep 300ms
Enter
Sleep 500ms
Down
Enter
Sleep 500ms
Down
Sleep 1.2s
Escape
Sleep 600ms

# Fullscreen graph, walk to the pod, search
Type "g"
Sleep 1.2s
Type "l"
Sleep 400ms
Type "l"
Sleep 400ms
Type "l"
Sleep 1.2s
Type "/svc"
Sleep 800ms
Enter
Sleep 1s
Escape
Sleep 300ms
Escape
Sleep 600ms

# Describe: last sync error + history
Type "d"
Sleep 2.5s
Escape
Sleep 400ms

# Colored YAML
Type "y"
Sleep 2s
Escape
Sleep 800ms

Ctrl+C
Sleep 300ms
