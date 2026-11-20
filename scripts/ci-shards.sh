#!/bin/sh
# B777: split the cmd/code-forge-native tests over CI shards (the package's tests take ~11 min under
# -race in one process; nothing else in native/ is over ~1.5 min).
#
# Method: round-robin over the sorted test list. Test k (0-based, LC_ALL=C order) goes to shard
# k mod N. The list comes from `go test -list` at run time, so a new test lands in a shard with no
# hand list and no timing file to keep current. Round-robin spreads each B-numbered family of
# neighbouring names (often slow together) over all shards, which balances well enough without a
# committed timing file that would go stale with every block.
#
# Run from native/. RACE (empty or -race) is passed to `go test -list` as a flag, so -list and the
# shard's `go test $RACE -run ...` share one build. Not GOFLAGS: tests that run `go build` with
# CGO_ENABLED=0 inherit GOFLAGS, and -race needs cgo.
#   sh ../scripts/ci-shards.sh list              sorted test names (Test, Example, Fuzz)
#   sh ../scripts/ci-shards.sh names I N         names of shard I (0-based) of N
#   sh ../scripts/ci-shards.sh regex I N         anchored -run regex for shard I of N
#   sh ../scripts/ci-shards.sh check N           proves: each shard's regex selects exactly its names,
#                                                no name is in two shards, union == full list
#   sh ../scripts/ci-shards.sh pick I N < names  shard I of N of the names on stdin (no go call); the
#                                                split rule for local shards too (cf-gotest --shards, B807)
set -eu
pkg=./cmd/code-forge-native

die() { echo "ci-shards: $*" >&2; exit 1; }
case ${RACE:-} in "" | -race) ;; *) die "RACE must be empty or -race, got: $RACE" ;; esac

list() {
	out=$(go test ${RACE:-} -list '.*' "$pkg") || { printf '%s\n' "$out" >&2; die "go test -list failed"; }
	printf '%s\n' "$out" | tests_only
}

num() { case $1 in '' | *[!0-9]*) die "not a number: $1" ;; esac; }

pick() { # stdin: sorted names; $1 index, $2 count
	awk -v i="$1" -v n="$2" '(NR - 1) % n == i'
}

tests_only() { # stdin: any lines -> sorted test names
	grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' | LC_ALL=C sort
}

to_regex() { # stdin: names -> ^(A|B)$
	paste -sd'|' - | sed 's/^/^(/; s/$/)$/'
}

shard_args() {
	[ $# -eq 2 ] || die "usage: $cmd I N"
	num "$1"; num "$2"
	[ "$2" -ge 1 ] || die "N must be >= 1"
	[ "$1" -lt "$2" ] || die "shard index $1 is not below $2"
}

cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
list)
	list
	;;
names)
	shard_args "$@"
	list | pick "$1" "$2"
	;;
pick)
	shard_args "$@"
	{ tests_only || true; } | pick "$1" "$2"
	;;
regex)
	shard_args "$@"
	names=$(list | pick "$1" "$2")
	[ -n "$names" ] || die "shard $1 of $2 has no tests"
	printf '%s\n' "$names" | to_regex
	;;
check)
	[ $# -eq 1 ] || die "usage: check N"
	num "$1"
	n=$1
	[ "$n" -ge 1 ] || die "N must be >= 1"
	full=$(list)
	total=$(printf '%s\n' "$full" | grep -c .)
	[ "$total" -gt 0 ] || die "empty test list"
	all=""
	i=0
	while [ "$i" -lt "$n" ]; do
		want=$(printf '%s\n' "$full" | pick "$i" "$n")
		[ -n "$want" ] || die "shard $i of $n has no tests"
		re=$(printf '%s\n' "$want" | to_regex)
		got=$(go test ${RACE:-} -list "$re" "$pkg" | grep -E '^(Test|Example|Fuzz)[A-Za-z0-9_]*$' | LC_ALL=C sort)
		[ "$got" = "$want" ] || {
			printf 'want/got diff for shard %s:\n' "$i" >&2
			printf '%s\n' "$want" >"${TMPDIR:-/tmp}/ci-shards-want.$$"
			printf '%s\n' "$got" | diff "${TMPDIR:-/tmp}/ci-shards-want.$$" - >&2 || true
			rm -f "${TMPDIR:-/tmp}/ci-shards-want.$$"
			die "shard $i regex does not select exactly its names"
		}
		echo "shard $i: $(printf '%s\n' "$got" | grep -c .) tests"
		all=$(printf '%s\n%s' "$all" "$got")
		i=$((i + 1))
	done
	all=$(printf '%s\n' "$all" | grep . | LC_ALL=C sort)
	dups=$(printf '%s\n' "$all" | uniq -d)
	[ -z "$dups" ] || die "in two shards: $dups"
	[ "$all" = "$full" ] || die "union of $n shards is not the full list"
	echo "ci-shards: $n shards, $total tests, union == full list, no test in two shards"
	;;
*)
	die "usage: list | names I N | regex I N | check N | pick I N"
	;;
esac
