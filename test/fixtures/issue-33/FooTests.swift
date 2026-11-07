import XCTest

final class FooTests: XCTestCase {
    func testFormatsReference() {
        // FooTests.swift:34
        let reference = "invoice-0042"
        let formatted = reference.uppercased()
        XCTAssertEqual(formatted, "INVOICE-0042")
    }

    func testPreservesEmptyReference() {
        XCTAssertEqual("".uppercased(), "")
    }
}
