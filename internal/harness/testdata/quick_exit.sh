#!/bin/bash
# Fixture for the stdin-writer goroutine join check: exits immediately,
# closing stdout right away (fast, clean EOF) so harness.Run completes its
# normal (non-cancelled) path with minimal delay.
exit 0
