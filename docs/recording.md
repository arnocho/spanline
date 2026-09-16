# Recording the interface

The tapes in this directory drive [vhs](https://github.com/charmbracelet/vhs):

```
vhs docs/demo.tape    # the walkthrough
vhs docs/still.tape   # one screenshot per screen
```

Note the paths inside a tape must be quoted, or the parser splits them on the slashes and fails
with a confusing error.

On this machine the recording does not currently produce a file: vhs 0.12 prints "Creating
demo.gif" and exits cleanly having written nothing, with ffmpeg 7 and with ffmpeg 9 alike, so the
cause is upstream of the encoder rather than in these tapes. Until that is resolved, the screens
are covered by deterministic frame dumps instead, which are the same bytes the interface draws:

```
SPANLINE_FRAME_DIR=/tmp/frames go test ./internal/ui/ -run TestDumpFrames
```

That writes the reading animation, the three views, a mid animation frame and a narrow terminal
frame, and the test fails if any line overflows the terminal or if a screen loses its answer.
