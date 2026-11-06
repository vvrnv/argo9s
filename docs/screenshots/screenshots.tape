# README screenshots — one PNG per view, from offline demo data (no cluster).
# Render: make screenshots   (needs vhs + ttyd: https://github.com/charmbracelet/vhs)

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1440
Set Height 820
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 20ms

Env CLICOLOR_FORCE "1"
Env COLORTERM "truecolor"

Hide
Type "./bin/argo9s --demo" Enter
Sleep 2s
Show

# Applications (default view)
Sleep 1s
Screenshot docs/screenshots/apps.png
Sleep 600ms

# Live "/" filter
Type "/guest"
Sleep 1s
Screenshot docs/screenshots/filter.png
Sleep 600ms
Escape
Sleep 300ms

# ApplicationSets, then drill into one
Type ":appsets" Enter
Sleep 1s
Screenshot docs/screenshots/appsets.png
Sleep 600ms
Enter
Sleep 1s
Screenshot docs/screenshots/appset-apps.png
Sleep 600ms
Escape
Sleep 300ms

Type ":projects" Enter
Sleep 1s
Screenshot docs/screenshots/projects.png
Sleep 600ms

Type ":clusters" Enter
Sleep 1s
Screenshot docs/screenshots/clusters.png
Sleep 600ms

Type ":ctx" Enter
Sleep 1s
Screenshot docs/screenshots/contexts.png
Sleep 600ms

# Back to apps — api-gateway (degraded) is first
Type ":apps" Enter
Sleep 500ms

Type "d"
Sleep 1s
Screenshot docs/screenshots/describe.png
Sleep 600ms
Escape
Sleep 300ms

Type "y"
Sleep 1s
Screenshot docs/screenshots/yaml.png
Sleep 600ms
Escape
Sleep 300ms

# Managed resources tree: expand deploy ▸ rs ▸ pod
Enter
Sleep 800ms
Down 2
Enter
Down
Enter
Down
Sleep 1s
Screenshot docs/screenshots/tree.png
Sleep 600ms

# Pod with two containers → picker → logs
Type "l"
Sleep 1s
Screenshot docs/screenshots/containers.png
Sleep 600ms
Enter
Sleep 1s
Screenshot docs/screenshots/logs.png
Sleep 600ms
Escape
Sleep 300ms
Escape
Sleep 300ms
Escape
Sleep 300ms

# Fullscreen graph: walk to the pod, then search
Type "g"
Sleep 800ms
Type "lll"
Sleep 1s
Screenshot docs/screenshots/graph.png
Sleep 600ms
Type "/svc"
Sleep 500ms
Enter
Sleep 1s
Screenshot docs/screenshots/graph-search.png
Sleep 600ms
Escape
Sleep 200ms
Escape
Sleep 300ms

Type "?"
Sleep 1s
Screenshot docs/screenshots/help.png
Sleep 600ms

Ctrl+C
Sleep 300ms
