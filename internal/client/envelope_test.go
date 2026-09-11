package client

import "testing"

// The case field-setters (RenameCase, ChangeCaseAddress, ChangeCaseZip,
// RenameCaseCustomerPhoneNumber) answer {"success":true,...} -- a FOURTH
// success shape, alongside {"status":"Success"}, a bare data object, and a
// JSON array.
//
// That matters because errorEnvelope only recognises {"status":"Error"}. If a
// refused case write answers {"success":false}, it would be read as a success
// and only read-back would notice -- exactly the failure the 2026-09-04
// envelope fix existed to eliminate, reintroduced through a different shape.
func TestErrorEnvelope_ShapesFromTheCaseEndpoints(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"case setter success", `{"success":true,"newCaseName":"Test 1234"}`, false},
		{"case setter refusal", `{"success":false}`, true},
		{"case setter refusal with message", `{"success":false,"message":"Sagen er låst."}`, true},

		// Regressions: everything already relied upon must keep its meaning.
		{"status success", `{"status":"Success"}`, false},
		{"status error", `{"status":"Error","message":"nope"}`, true},
		{"bare data object", `{"caseId":4,"caseNumber":"KA-4"}`, false},
		{"array", `[{"workerNr":1}]`, false},
		{"empty", ``, false},

		// `success` absent must NOT read as false -- every existing response
		// omits it, and treating omission as failure would break all of them.
		{"absent success field", `{"customerId":4}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := errorEnvelope([]byte(tc.body), nil)
			if tc.wantErr && err == nil {
				t.Errorf("body %s was accepted as success", tc.body)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("body %s was rejected: %v", tc.body, err)
			}
		})
	}
}
