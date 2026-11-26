package engine

import "testing"

// Only Andaru's own services are named on the services socket: root's realize, or any other account, is refused.
func TestOnlyServiceUsersUseTheServicesSocket(t *testing.T) {
	for _, name := range []string{"realize", "root", "andaru-web"} {
		if err := isService(name); err == nil {
			t.Errorf("isService(%q) accepted it", name)
		}
	}
	for _, name := range serviceUsers {
		if err := isService(name); err != nil {
			t.Errorf("isService(%q) = %v", name, err)
		}
		if _, granted := busGrants[name]; !granted || name == "realize" {
			t.Errorf("service user %q has no grants of its own", name)
		}
	}
}
