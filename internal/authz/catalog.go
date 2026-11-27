package authz

import "slices"

type Capability string

const (
	AddonsManage         Capability = "addons.manage"
	AddonsRead           Capability = "addons.read"
	AuditRead            Capability = "audit.read"
	BackupsCreate        Capability = "backups.create"
	BackupsExportSecrets Capability = "backups.exportSecrets"
	BackupsRestore       Capability = "backups.restore"
	ConfigurationApply   Capability = "configuration.apply"
	ConfigurationConfirm Capability = "configuration.confirm"
	ConfigurationRead    Capability = "configuration.read"
	ConfigurationWrite   Capability = "configuration.write"
	NotificationsManage  Capability = "notifications.manage"
	NotificationsRead    Capability = "notifications.read"
	StatusRead           Capability = "status.read"
	TokensManageAll      Capability = "tokens.manageAll"
	TokensManageSelf     Capability = "tokens.manageSelf"
	UsersManage          Capability = "users.manage"
	UsersRead            Capability = "users.read"
)

var catalog = []Capability{
	AddonsManage,
	AddonsRead,
	AuditRead,
	BackupsCreate,
	BackupsExportSecrets,
	BackupsRestore,
	ConfigurationApply,
	ConfigurationConfirm,
	ConfigurationRead,
	ConfigurationWrite,
	NotificationsManage,
	NotificationsRead,
	StatusRead,
	TokensManageAll,
	TokensManageSelf,
	UsersManage,
	UsersRead,
}

func Catalog() []Capability { return slices.Clone(catalog) }
