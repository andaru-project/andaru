package authz

import "slices"

type Capability string

const (
	AuditRead             Capability = "audit.read"
	BackupsCreate         Capability = "backups.create"
	BackupsExportSecrets  Capability = "backups.exportSecrets"
	BackupsRestore        Capability = "backups.restore"
	ConfigurationApply    Capability = "configuration.apply"
	ConfigurationConfirm  Capability = "configuration.confirm"
	ConfigurationRead     Capability = "configuration.read"
	ConfigurationWrite    Capability = "configuration.write"
	StatusRead            Capability = "status.read"
	TokensManageAll       Capability = "tokens.manageAll"
	TokensManageSelf      Capability = "tokens.manageSelf"
	UsersManage           Capability = "users.manage"
	UsersRead             Capability = "users.read"
)

var catalog = []Capability{
	AuditRead,
	BackupsCreate,
	BackupsExportSecrets,
	BackupsRestore,
	ConfigurationApply,
	ConfigurationConfirm,
	ConfigurationRead,
	ConfigurationWrite,
	StatusRead,
	TokensManageAll,
	TokensManageSelf,
	UsersManage,
	UsersRead,
}

func Catalog() []Capability { return slices.Clone(catalog) }
