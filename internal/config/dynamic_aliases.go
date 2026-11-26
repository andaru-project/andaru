package config

import "context"

// DynamicAlias is a saved address alias fed from a URL or from host names.
type DynamicAlias struct {
	ID, Name, Kind, URL string
	Names               []string
	RefreshSeconds      int
}

// DynamicAliases returns the address aliases of the saved head fed from a URL or from host names.
func (s *Store) DynamicAliases(ctx context.Context) (result []DynamicAlias, err error) {
	err = s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		for _, a := range state.Config.Aliases.Addresses {
			if a.dynamic() {
				result = append(result, DynamicAlias{ID: a.ID, Name: a.Name, Kind: a.Kind, URL: a.URL, Names: a.Names, RefreshSeconds: a.RefreshSeconds})
			}
		}
		return nil
	})
	return
}
