package uploader

import "net/http"

// EmitAuditForTest is a test-only wrapper that exposes the unexported emitAudit
// function to the external test package (uploader_test).
func EmitAuditForTest(a *App, r *http.Request, action, resource string, status int) {
	emitAudit(a, r, action, resource, status)
}
