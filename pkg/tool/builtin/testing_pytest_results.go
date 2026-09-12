package builtin

import (
	"bytes"
	"encoding/xml"
	"io"
)

func parsePytestReport(raw []byte) testReport {
	var root struct {
		XMLName xml.Name `xml:"testsuites"`
		Suites  []struct {
			Cases []struct {
				Failures []struct{} `xml:"failure"`
				Errors   []struct{} `xml:"error"`
				Skips    []struct{} `xml:"skipped"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&root); err != nil || len(root.Suites) == 0 {
		return testReport{}
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return testReport{}
		}
		switch value := token.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(value)) != 0 {
				return testReport{}
			}
		case xml.Comment, xml.ProcInst:
		default:
			return testReport{}
		}
	}
	report := testReport{complete: true}
	for _, suite := range root.Suites {
		for _, tc := range suite.Cases {
			switch {
			case len(tc.Failures) > 0 || len(tc.Errors) > 0:
				report.failed++
			case len(tc.Skips) > 0:
				report.skipped++
			default:
				report.passed++
			}
		}
	}
	return report
}
