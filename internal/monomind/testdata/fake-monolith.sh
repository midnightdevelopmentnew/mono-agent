#!/bin/sh
# Monolithic fake monomind for the process-group kill test: spawns a
# long-lived grandchild, then ignores stdin forever. Only a group kill can
# reap both processes.
sleep 60 &
CHILD=$!
wait $CHILD
