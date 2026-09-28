package httpapi

import (
	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
	"github.com/nisha-ts-40599/blink-backend/internal/project"
)

func canonicalDigestStakeholders(stakes []project.StakeholderRequest) string {
	rows := make([]map[string]string, 0, len(stakes))
	for _, st := range stakes {
		rows = append(rows, map[string]string{
			"roleCode": st.RoleCode,
			"name":     st.Name,
			"email":    st.Email,
		})
	}
	return canonical.DigestJSON(rows)
}
