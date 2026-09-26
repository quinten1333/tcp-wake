#!/usr/bin/env python3
"""Check — and where it is mechanical, repair — the derived tables in an
architecture document.

The architecture document contains two tables that restate facts stored
elsewhere. Restating them by hand is the main source of drift, so this script
owns them:

  * The §9 ADR index is derived from the ADR files. `--fix` rebuilds it.
  * The coverage appendix is derived from the spec's requirement list. `--fix`
    adds a row for any requirement that has none.

What `--fix` will NOT do, because it needs judgement:
  * Fill in an "Addressed by" cell. Only you know which element covers a
    requirement, and a wrong guess is worse than a blank.
  * Delete a coverage row for a requirement that is no longer in the spec. That
    may be a mistake upstream, so it is reported, not removed.

Usage:
    python3 check_traceability.py <spec.md> <architecture.md> [--adr-dir DIR]
    python3 check_traceability.py <spec.md> <architecture.md> --fix

The ADR directory defaults to `adr/` next to the architecture document.

Exit codes:
    0  no hard findings (notes may still be printed)
    1  at least one hard finding remains
    2  a file could not be read, or the document structure could not be parsed

Severity levels:
    [finding]   a real gap — resolve it or consciously accept it
    [note]      expected or informational; a proposed ADR is a note, not a defect
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

# Requirement IDs as written in a spec table: FR-1, NFR-2, IF-3, or any other
# prefix the project invents. Deliberately permissive so a new requirement type
# needs no edit here. ADR ids are excluded by the caller.
REQ_ID_RE = re.compile(r"^[A-Z][A-Z0-9]*-\d+$")
REQ_ID_ANYWHERE_RE = re.compile(r"\b[A-Z][A-Z0-9]*-\d+\b")

# ADR IDs as written in the document: ADR-0001 or ADR-1.
ADR_ID_RE = re.compile(r"^ADR-(\d+)$", re.IGNORECASE)

# The appendix heading the template defines. Matched loosely on the words, so
# "Appendix: Requirement Coverage" and "## Appendix — Requirement coverage"
# both work, but a random table elsewhere is not mistaken for it.
COVERAGE_HEADING_RE = re.compile(r"^#{1,6}\s*.*requirement\s+coverage.*$", re.IGNORECASE)

# The ADR index section heading, e.g. "## 9. Architectural Decisions".
ADR_INDEX_HEADING_RE = re.compile(r"^#{1,6}\s*\d*\.?\s*architectural\s+decisions.*$", re.IGNORECASE)

ANY_HEADING_RE = re.compile(r"^#{1,6}\s")

# Markers the `--fix` mode rewrites between. HTML comments render invisibly, so
# they are safe to leave in the document.
ADR_INDEX_START = "<!-- adr-index:start -->"
ADR_INDEX_END = "<!-- adr-index:end -->"

# Status values from the MADR lifecycle.
VALID_STATUSES = {"proposed", "accepted", "rejected", "deprecated", "superseded"}

# Columns rendered into the ADR index, in order.
INDEX_HEADER = ["ADR", "Title", "Status", "Driver", "Date"]


# --------------------------------------------------------------------------- #
# markdown helpers
# --------------------------------------------------------------------------- #

def cells(line: str) -> list[str]:
    """Split a markdown table row into trimmed cells."""
    stripped = line.strip()
    if stripped.startswith("|"):
        stripped = stripped[1:]
    if stripped.endswith("|"):
        stripped = stripped[:-1]
    return [c.strip() for c in stripped.split("|")]


def clean(cell: str) -> str:
    """Strip markdown emphasis and code ticks from a cell."""
    return cell.replace("`", "").replace("*", "").strip()


def is_separator_row(parts: list[str]) -> bool:
    """True for the |---|---|---| row under a markdown table header."""
    return bool(parts) and all(re.fullmatch(r":?-{2,}:?", p.strip()) for p in parts if p.strip())


def is_requirement_id(text: str) -> bool:
    """True for a requirement ID, and false for an ADR ID."""
    return bool(REQ_ID_RE.match(text)) and not text.upper().startswith("ADR")


def find_section(text: str, heading_re: re.Pattern[str]) -> tuple[int, int] | None:
    """Return (start_line, end_line) of the section body, or None if absent.

    start_line is the first line after the heading; end_line is the line index of
    the next heading (or the end of the file).
    """
    lines = text.splitlines()
    start = None
    for i, line in enumerate(lines):
        if heading_re.match(line):
            start = i + 1
            break
    if start is None:
        return None
    end = len(lines)
    for j in range(start, len(lines)):
        if ANY_HEADING_RE.match(lines[j]):
            end = j
            break
    return start, end


def section_rows(text: str, heading_re: re.Pattern[str]) -> list[list[str]] | None:
    """Return the table rows inside the first section matching `heading_re`."""
    span = find_section(text, heading_re)
    if span is None:
        return None
    lines = text.splitlines()
    rows: list[list[str]] = []
    for line in lines[span[0]:span[1]]:
        if not line.strip().startswith("|"):
            continue
        parts = cells(line)
        if not parts or is_separator_row(parts):
            continue
        rows.append(parts)
    return rows


# --------------------------------------------------------------------------- #
# parsing
# --------------------------------------------------------------------------- #

def parse_requirements(spec_text: str) -> dict[str, str]:
    """Map requirement ID -> statement, from every requirement table in the spec."""
    found: dict[str, str] = {}
    for line in spec_text.splitlines():
        if not line.strip().startswith("|"):
            continue
        parts = cells(line)
        if len(parts) < 2 or is_separator_row(parts):
            continue
        req_id = clean(parts[0])
        if is_requirement_id(req_id):
            found[req_id] = clean(parts[1])
    return found


def parse_coverage(arch_text: str) -> dict[str, str] | None:
    """Map requirement ID -> 'addressed by' text, from the coverage appendix."""
    rows = section_rows(arch_text, COVERAGE_HEADING_RE)
    if rows is None:
        return None
    coverage: dict[str, str] = {}
    for parts in rows:
        req_id = clean(parts[0])
        if not is_requirement_id(req_id):
            continue  # header row or prose
        coverage[req_id] = clean(parts[1]) if len(parts) > 1 else ""
    return coverage


def parse_adr_index(arch_text: str) -> dict[str, str] | None:
    """Map ADR ID -> status, from the architectural decisions section."""
    rows = section_rows(arch_text, ADR_INDEX_HEADING_RE)
    if rows is None:
        return None
    index: dict[str, str] = {}
    for parts in rows:
        adr_id = clean(parts[0]).upper()
        if not ADR_ID_RE.match(adr_id):
            continue
        status = clean(parts[2]).lower() if len(parts) > 2 else ""
        index[adr_id] = status
    return index


def parse_status_from_file(text: str) -> str:
    """Read the `status:` value from an ADR file's frontmatter.

    An unedited MADR placeholder like "{proposed | accepted | ...}" is reported
    as unknown rather than as a real status, so it does not produce a spurious
    mismatch against the index.
    """
    front = text
    m = re.match(r"^---\n(.*?)\n---", text, re.S)
    if m:
        front = m.group(1)

    m = re.search(r"^\s*status:\s*(.+)$", front, re.MULTILINE | re.IGNORECASE)
    if not m:
        return ""

    raw = m.group(1).strip().strip("\"'").lower()
    if "{" in raw or "|" in raw:
        return ""
    for status in VALID_STATUSES:
        if raw.startswith(status):
            return status
    return raw


def parse_date_from_file(text: str) -> str:
    """Read the `date:` value from frontmatter, or '' if absent."""
    m = re.match(r"^---\n(.*?)\n---", text, re.S)
    front = m.group(1) if m else text
    m = re.search(r"^\s*date:\s*(.+)$", front, re.MULTILINE | re.IGNORECASE)
    if not m:
        return ""
    raw = m.group(1).strip().strip("\"'")
    return "" if "{" in raw else raw


def parse_title_from_file(text: str) -> str:
    """First H1, with a leading 'ADR-NNNN:' prefix stripped."""
    for line in text.splitlines():
        if line.startswith("# "):
            title = line[2:].strip()
            title = re.sub(r"^ADR[-\s]*\d+\s*[:\-—]\s*", "", title, flags=re.IGNORECASE)
            return title
    return ""


def parse_adr_file_details(path: Path) -> dict[str, str] | None:
    """Read one ADR file into the fields the index needs."""
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return None

    m = re.match(r"^(\d+)[-_]", path.name)
    if not m:
        return None
    adr_id = f"ADR-{int(m.group(1)):04d}"

    # The driver is any requirement ID cited anywhere in the ADR — Context,
    # Decision Drivers, or Links all legitimately name them.
    drivers: list[str] = []
    for found in REQ_ID_ANYWHERE_RE.findall(text):
        if is_requirement_id(found) and found not in drivers:
            drivers.append(found)

    return {
        "id": adr_id,
        "title": parse_title_from_file(text),
        "status": parse_status_from_file(text),
        "date": parse_date_from_file(text),
        "driver": ", ".join(sorted(drivers, key=id_sort_key)),
    }


def parse_adr_files(adr_dir: Path) -> dict[str, dict[str, str]]:
    """Map ADR ID -> fields, for every NNNN-*.md file in the directory."""
    files: dict[str, dict[str, str]] = {}
    if not adr_dir.is_dir():
        return files
    for path in sorted(adr_dir.glob("*.md")):
        details = parse_adr_file_details(path)
        if details:
            files[details["id"]] = details
    return files


# --------------------------------------------------------------------------- #
# sorting
# --------------------------------------------------------------------------- #

def id_sort_key(req_id: str) -> tuple[str, int]:
    """Sort FR-2 before FR-10, and group by prefix."""
    prefix, _, num = req_id.partition("-")
    return (prefix, int(num) if num.isdigit() else 0)


def adr_sort_key(adr_id: str) -> int:
    m = ADR_ID_RE.match(adr_id)
    return int(m.group(1)) if m else 0


# --------------------------------------------------------------------------- #
# rendering (the --fix half)
# --------------------------------------------------------------------------- #

def render_index_table(adr_files: dict[str, dict[str, str]]) -> list[str]:
    """Render the ADR index as markdown table lines."""
    lines = [
        "| " + " | ".join(INDEX_HEADER) + " |",
        "|" + "|".join(["---"] * len(INDEX_HEADER)) + "|",
    ]
    for adr_id in sorted(adr_files, key=adr_sort_key):
        f = adr_files[adr_id]
        lines.append(
            "| {id} | {title} | {status} | {driver} | {date} |".format(
                id=adr_id,
                title=f.get("title") or "",
                status=f.get("status") or "proposed",
                driver=f.get("driver") or "",
                date=f.get("date") or "",
            )
        )
    return lines


def rebuild_index(arch_text: str, adr_files: dict[str, dict[str, str]]) -> tuple[str, str]:
    """Replace the content between the adr-index markers.

    Returns (new_text, message). new_text is unchanged if the markers are absent.
    """
    lines = arch_text.splitlines()
    try:
        start = lines.index(ADR_INDEX_START)
        end = lines.index(ADR_INDEX_END)
    except ValueError:
        return arch_text, "no adr-index markers found"

    if end < start:
        return arch_text, "adr-index markers are out of order"

    new_block = render_index_table(adr_files)
    result = lines[:start + 1] + new_block + lines[end:]
    return "\n".join(result) + ("\n" if arch_text.endswith("\n") else ""), "rebuilt"


def rebuild_coverage(arch_text: str, spec_ids: list[str]) -> tuple[str, str]:
    """Add a row for any spec requirement missing from the coverage table.

    Existing rows keep their 'Addressed by' text. Rows are re-sorted so a newly
    added requirement lands in the right place rather than at the bottom.
    """
    rows = section_rows(arch_text, COVERAGE_HEADING_RE)
    if rows is None:
        return arch_text, "no coverage appendix found"

    existing: dict[str, str] = {}
    phantom: dict[str, str] = {}
    for parts in rows:
        req_id = clean(parts[0])
        if not is_requirement_id(req_id):
            continue
        by = clean(parts[1]) if len(parts) > 1 else ""
        if req_id in spec_ids:
            existing[req_id] = by
        else:
            phantom[req_id] = by  # stale; reported, never deleted

    added = [r for r in spec_ids if r not in existing]
    ordered = sorted(set(spec_ids), key=id_sort_key)
    ordered += sorted(phantom, key=id_sort_key)

    new_rows = [f"| {r} | {existing.get(r, phantom.get(r, ''))} |" for r in ordered]

    # Rebuild the whole table: header, separator, then the rows.
    lines = arch_text.splitlines()
    span = find_section(arch_text, COVERAGE_HEADING_RE)
    assert span is not None  # rows is not None implies the section exists
    table_lines = [i for i in range(span[0], span[1]) if lines[i].strip().startswith("|")]
    if not table_lines:
        return arch_text, "coverage appendix has no table"

    first, last = table_lines[0], table_lines[-1]
    rebuilt = lines[:first] + ["| SRS ID | Addressed by (element / ADR) |", "|---|---|"] + new_rows + lines[last + 1:]
    result = "\n".join(rebuilt) + ("\n" if arch_text.endswith("\n") else "")

    if not added:
        return result, "no rows added"
    return result, f"added {len(added)} row(s): {', '.join(sorted(added, key=id_sort_key))}"


# --------------------------------------------------------------------------- #
# checking
# --------------------------------------------------------------------------- #

def check(spec_text: str, arch_text: str, adr_dir: Path) -> tuple[list[str], int]:
    """Return (report lines, hard finding count)."""
    report: list[str] = []
    hard = 0

    def finding(msg: str) -> None:
        nonlocal hard
        hard += 1
        report.append(f"  [finding] {msg}")

    def note(msg: str) -> None:
        report.append(f"  [note] {msg}")

    requirements = parse_requirements(spec_text)
    coverage = parse_coverage(arch_text)
    index = parse_adr_index(arch_text)
    adr_files = parse_adr_files(adr_dir)

    # --- Requirement coverage ------------------------------------------------
    report.append("Requirement coverage")
    if coverage is None:
        finding(
            "no 'Requirement Coverage' appendix found. Add the appendix heading and the "
            "| SRS ID | Addressed by | table so coverage can be checked."
        )
    elif not requirements:
        finding("no requirement rows found in the spec — nothing to trace against.")
    else:
        missing = sorted(set(requirements) - set(coverage), key=id_sort_key)
        unaddressed = sorted(
            (r for r, by in coverage.items() if not by and r in requirements), key=id_sort_key
        )
        phantom = sorted(set(coverage) - set(requirements), key=id_sort_key)

        if not missing and not unaddressed and not phantom:
            note(f"all {len(requirements)} requirements have a coverage row with an owner.")
        for req_id in missing:
            finding(f"{req_id} is in the spec but has no coverage row — run --fix to add the row.")
        for req_id in unaddressed:
            finding(f"{req_id} has a coverage row but nothing in the 'Addressed by' column — name the element or ADR.")
        for req_id in phantom:
            finding(f"{req_id} has a coverage row but is not in the spec — stale or mistyped ID.")
    report.append("")

    # --- ADR index integrity -------------------------------------------------
    report.append("ADR index")
    if index is None:
        finding("no 'Architectural Decisions' section found — the ADR index is missing.")
    else:
        if not index:
            finding("the ADR index has no ADR rows.")
        if ADR_INDEX_START not in arch_text:
            finding(
                "no adr-index markers. Wrap the §9 table in "
                f"`{ADR_INDEX_START}` and `{ADR_INDEX_END}` so --fix can rebuild it."
            )
        for adr_id in sorted(index, key=adr_sort_key):
            status = index[adr_id]
            if status and status not in VALID_STATUSES:
                finding(f"{adr_id} has status '{status}' in the index, which is not one of {sorted(VALID_STATUSES)}.")
            if not adr_files:
                continue
            if adr_id not in adr_files:
                finding(f"{adr_id} is in the index but has no ADR file in {adr_dir}/.")
                continue
            file_status = adr_files[adr_id].get("status", "")
            if file_status and status and file_status != status:
                finding(
                    f"{adr_id} is '{status}' in the index but '{file_status}' in the file — "
                    "run --fix to rebuild the index."
                )
            elif status == "proposed":
                note(f"{adr_id} is proposed — an open question; make sure the walk-through names it.")

        for adr_id in sorted(adr_files, key=adr_sort_key):
            if adr_id in index:
                continue
            if adr_files[adr_id].get("status") == "proposed":
                note(f"{adr_id} is a proposed ADR not yet in the index — expected until it is accepted.")
            else:
                finding(f"{adr_id} exists as a file but is missing from the index — run --fix.")
    report.append("")

    return report, hard


def read_text(path: Path, label: str) -> str | None:
    """Read a file, reporting why on failure. Returns None on any error."""
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError:
        print(f"error: no such {label}: {path}", file=sys.stderr)
    except IsADirectoryError:
        print(f"error: {label} is a directory, not a file: {path}", file=sys.stderr)
    except PermissionError:
        print(f"error: permission denied reading {label}: {path}", file=sys.stderr)
    except UnicodeDecodeError as exc:
        print(f"error: {label} is not valid UTF-8 text: {path} ({exc})", file=sys.stderr)
    return None


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Check, and where mechanical repair, the derived tables in an architecture document."
    )
    parser.add_argument("spec", help="path to the requirements specification markdown file")
    parser.add_argument("architecture", help="path to the architecture markdown file")
    parser.add_argument(
        "--adr-dir",
        default=None,
        help="directory holding the ADR files (default: adr/ next to the architecture file)",
    )
    parser.add_argument(
        "--fix",
        action="store_true",
        help="rebuild the ADR index and add missing coverage rows before checking",
    )
    args = parser.parse_args()

    spec_path = Path(args.spec)
    arch_path = Path(args.architecture)
    adr_dir = Path(args.adr_dir) if args.adr_dir else arch_path.parent / "adr"

    spec_text = read_text(spec_path, "spec")
    if spec_text is None:
        return 2
    arch_text = read_text(arch_path, "architecture document")
    if arch_text is None:
        return 2

    if args.fix:
        adr_files = parse_adr_files(adr_dir)
        spec_ids = sorted(parse_requirements(spec_text), key=id_sort_key)

        new_text, index_msg = rebuild_index(arch_text, adr_files)
        new_text, cov_msg = rebuild_coverage(new_text, spec_ids)

        print("Repair")
        print(f"  ADR index: {index_msg}")
        print(f"  Coverage: {cov_msg}")

        if new_text != arch_text:
            try:
                arch_path.write_text(new_text, encoding="utf-8")
            except OSError as exc:
                print(f"error: could not write {arch_path}: {exc}", file=sys.stderr)
                return 2
            print(f"  wrote {arch_path}")
        else:
            print("  no changes needed")
        print()

        arch_text = new_text

    report, hard = check(spec_text, arch_text, adr_dir)

    if hard == 0:
        print("Traceability check passed. Notes below are informational.\n")
    else:
        print(f"Traceability check found {hard} problem(s) to resolve.\n")
    print("\n".join(report).rstrip())
    return 1 if hard else 0


if __name__ == "__main__":
    sys.exit(main())
