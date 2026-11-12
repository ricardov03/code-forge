package main

// fcRule is the RULE text of src/session/facts.mjs (the facts delegate's
// packet head), generated from Node's buildFactsPacket([]) (B556).
var fcRule = "" +
	"# Facts delegate\n" +
	"\n" +
	"For each claim below, run the cheapest READ-ONLY check (`grep -rn -- <flag> <path>`, `which <cli>`,\n" +
	"`ls <path>`, `npm view <name> version`). Quote the exact command and the output line it printed (at most 200\n" +
	"characters). Tag each claim VERIFIED (the output shows it), NOT-FOUND (the check ran and it is absent) or\n" +
	"UNVERIFIABLE (no read-only check can decide it — say why in `why`). Never infer, never guess: the excerpt is the\n" +
	"command's literal output, never your reading of it. For a path use `ls <path>` (it prints the path); `test` prints\n" +
	"nothing, so a `test` check can never be VERIFIED. A flag is checked in the source text of the project, never by\n" +
	"running a program: `grep -rn -- <flag> <path>` (`<path>` a top-level source folder such as `src`, never `.`,\n" +
	"the root, docs, plans or tests) — VERIFIED only with a hit line (`file:line:text`) from a source file (not\n" +
	"Markdown or text, not docs, plans, tests or fixtures) that contains the flag, as the excerpt. Never run a program with `--help`, `-h` or `--version`: a version is checked with `npm view <name>\n" +
	"version`, `which` or `ls`. Run one command at a time, never a pipe. Only these forms are accepted: which,\n" +
	"command -v, ls, cat, head, tail, wc, grep (options only -n -c -i -w -F; recursive only as\n" +
	"`grep -rn -- <flag> <path>`),\n" +
	"find (only -name -iname -path -ipath -type -maxdepth -mindepth -print -print0 -newer -size -empty -mtime -mmin\n" +
	"-not ! -a -o -and -or, plus paths), stat, file, uname, sw_vers, jq, git\n" +
	"log|show|status|rev-parse|ls-files|cat-file, npm view|ls, test -e|-f|-d|-n, `curl -sI <one URL>`.\n" +
	"A check that breaks these rules is not counted.\n" +
	"A source root is the project (`.`) or `_sources/<label>`. A top-level source folder is directly\n" +
	"under that root: `src` or `_sources/<label>/app`. `_sources` itself is never a source folder.\n" +
	"An identifier symbol needs `grep -rn -- <quoted token or keyword token> <top-level source folder>`.\n" +
	"Quote the pattern. Keywords: class interface trait enum struct type function func def.\n" +
	"VERIFIED needs a file:line: hit in a source file holding the whole token; NOT-FOUND needs a whole top-level source folder.\n" +
	"A file-name symbol needs `find <root> -name <token>`; VERIFIED needs an output path ending in /<token>.\n" +
	"For a glob path use the per-claim check command. A find directory must be literal and its pattern quoted.\n" +
	"If a directory segment has glob characters, only `ls -d <exact glob token>` is accepted. For any glob claim,\n" +
	"`ls -d <exact glob token>` is also accepted, quoted or unquoted. VERIFIED needs a matching output path;\n" +
	"NOT-FOUND needs No such file output. Use the check path given for a source, never its live absolute path.\n" +
	"No `$`, no backticks, no redirection, no unquoted glob except the exact ls -d form above, no `~`, no absolute path outside /usr, /opt/homebrew or\n" +
	"/bin, no secret files.\n" +
	"An env claim is checked ONLY with `printenv NAME >/dev/null` (quote its exit status; the value is never shown).\n" +
	"\n" +
	"Reply with ONE JSON object: {\"facts\": [{\"fact_id\", \"claim\", \"kind\", \"command\", \"output_excerpt\", \"tag\", \"why\"}]} —\n" +
	"one row per claim: `fact_id` (F<n>) and `kind` copied from the list, `claim` = the token alone (the text after\n" +
	"`<kind>: `), `why` null unless UNVERIFIABLE.\n" +
	"The claim is the token alone, without source/check annotations or the bracketed glob check hint.\n" +
	"\n" +
	"## Claims\n"
