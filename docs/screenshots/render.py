#!/usr/bin/env python3
"""Turns a tmux capture (capture-pane -e) into an HTML page that looks like a
terminal window. capture.sh screenshots the page with headless Chromium."""
import html
import re
import sys

# xterm's 16 colors, then the 256-color cube and grays.
BASE = ["#1d1f21", "#cc6666", "#8fbf5f", "#e6c35c", "#5f87d7", "#b294bb", "#5fb3b3", "#c5c8c6",
        "#6b7078", "#ff6e6e", "#7ee07e", "#ffe066", "#82aaff", "#d7a6ff", "#6ee7f0", "#ffffff"]


def color(n):
    if n < 16:
        return BASE[n]
    if n < 232:
        n -= 16
        steps = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (steps[n // 36], steps[n // 6 % 6], steps[n % 6])
    g = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (g, g, g)


FG, BG = "#d6d8da", "#16181c"


def line_html(line, st):
    out = []
    pos = 0
    for m in re.finditer(r"\x1b\[([0-9;]*)m|$", line):
        text = line[pos:m.start()]
        pos = m.end()
        if text:
            fg, bg = st["fg"] or FG, st["bg"]
            if st["rev"]:
                fg, bg = bg or BG, fg
            css = [f"color:{fg}"]
            if bg:
                css.append(f"background:{bg}")
            if st["bold"]:
                css.append("font-weight:700")
            if st["under"]:
                css.append("text-decoration:underline")
            out.append(f'<span style="{";".join(css)}">{html.escape(text)}</span>')
        if m.group(1) is None:
            break
        codes = [int(c) if c else 0 for c in m.group(1).split(";")]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0:
                st.update(fg=None, bg=None, bold=False, under=False, rev=False)
            elif c == 1:
                st["bold"] = True
            elif c == 22:
                st["bold"] = False
            elif c == 4:
                st["under"] = True
            elif c == 24:
                st["under"] = False
            elif c == 7:
                st["rev"] = True
            elif c == 27:
                st["rev"] = False
            elif 30 <= c <= 37:
                st["fg"] = color(c - 30)
            elif 90 <= c <= 97:
                st["fg"] = color(c - 90 + 8)
            elif 40 <= c <= 47:
                st["bg"] = color(c - 40)
            elif 100 <= c <= 107:
                st["bg"] = color(c - 100 + 8)
            elif c == 39:
                st["fg"] = None
            elif c == 49:
                st["bg"] = None
            elif c in (38, 48) and i + 2 < len(codes) and codes[i + 1] == 5:
                st["fg" if c == 38 else "bg"] = color(codes[i + 2])
                i += 2
            elif c in (38, 48) and i + 4 < len(codes) and codes[i + 1] == 2:
                st["fg" if c == 38 else "bg"] = "#%02x%02x%02x" % tuple(codes[i + 2:i + 5])
                i += 4
            i += 1
    return "".join(out)


def main():
    lines = open(sys.argv[1], encoding="utf-8").read().rstrip("\n").split("\n")
    title = sys.argv[2] if len(sys.argv) > 2 else "sudo icefs"
    # tmux carries attributes from one line to the next, so the state does too.
    st = dict(fg=None, bg=None, bold=False, under=False, rev=False)
    body = "\n".join(line_html(l, st) for l in lines)
    print(f"""<!doctype html><meta charset="utf-8"><style>
html,body{{margin:0;background:transparent}}
.win{{display:inline-block;margin:24px;background:{BG};border-radius:10px;
  box-shadow:0 10px 30px rgba(0,0,0,.35),0 0 0 1px rgba(255,255,255,.06)}}
.bar{{height:34px;position:relative;font:13px system-ui,sans-serif;color:#8a8f98;text-align:center;line-height:34px}}
.dots{{position:absolute;left:14px;top:11px}}
.dots i{{display:inline-block;width:12px;height:12px;border-radius:50%;margin-right:7px}}
pre{{margin:0;padding:6px 22px 20px;font:14px/1.17 "DejaVu Sans Mono",Menlo,monospace;color:{FG}}}
</style><div class="win"><div class="bar"><span class="dots"><i style="background:#ff5f57"></i><i style="background:#febc2e"></i><i style="background:#28c840"></i></span>{html.escape(title)}</div><pre>{body}</pre></div>""")


main()
