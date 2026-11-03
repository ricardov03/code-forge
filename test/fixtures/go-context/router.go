package main

type server struct{}

func run() {
	label := "} \" {"
	raw := `} \` + "`" + ` {
func fakeRaw() {`
	runeBrace := '}'
	escapedRune := '\''
	// } fakeComment()
	/* { fakeBlock() } */
	if len(label) > 0 {
		println(raw, runeBrace, escapedRune)
	}
	println("RUN_END")
}

func (s server) serve() {
	println("}")
	println("METHOD_END")
}

func notCalled() {
	println("UNUSED_BODY")
}

func fakeRaw() { println("OPAQUE_RAW_BODY") }
func fakeComment() { println("OPAQUE_COMMENT_BODY") }
func fakeBlock() { println("OPAQUE_BLOCK_BODY") }
func fakeString() { println("OPAQUE_STRING_BODY") }
