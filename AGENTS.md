# Agent memory
This file is your memory. Update this file as you project progresses. You can create new sections as you see fit.

# Project architecture
_This section should hold the high level architecture of the project so that new AI's know where to look without having to read all the files._

# Learnings
_If you learn anything that is usefull to remember, update this file so that future AI's can benefit from it._

## System Environment
- Docker is pre-installed but the daemon must be started with `sudo dockerd` (needs root)
- The `arch` user needs `sudo usermod -aG docker arch` to access the docker socket without sudo
- Alpine Linux uses musl libc, not glibc — compiled Linux binaries from the host will NOT run inside Alpine containers

## Project Notes
- The project is a Go reverse-proxy called `tcp-wake`
- All specs are in `docs/specs/`, the todo list is `docs/todo.md`
- The git repo is at `/home/arch/code/`, docs are at `/home/arch/docs/`
- `docs/todo.md` is outside the git repo, so commits can't include todo changes
