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
	CertificatesManage   Capability = "certificates.manage"
	CertificatesRead     Capability = "certificates.read"
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
	VPNControl           Capability = "vpn.control"
	VPNRead              Capability = "vpn.read"
)

var catalog = []Capability{
	AddonsManage,
	AddonsRead,
	AuditRead,
	BackupsCreate,
	BackupsExportSecrets,
	BackupsRestore,
	CertificatesManage,
	CertificatesRead,
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
	VPNControl,
	VPNRead,
}

func Catalog() []Capability { return slices.Clone(catalog) }
