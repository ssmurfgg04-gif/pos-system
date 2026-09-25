package models

// PermissionDef describes one permission in the catalog. Roles are dynamic
// (admin-editable); these are the building blocks.
type PermissionDef struct {
        Key   string `json:"key"`
        Group string `json:"group"`
        Label string `json:"label"`
}

// PermissionCatalog is the full server-enforced permission list.
var PermissionCatalog = []PermissionDef{
        {Key: "pos.sell", Group: "Selling", Label: "Checkout and sell"},
        {Key: "pos.void", Group: "Selling", Label: "Void / cancel orders"},
        {Key: "pos.hold", Group: "Selling", Label: "Park and resume sales"},
        {Key: "orders.view", Group: "Selling", Label: "View order history"},
        {Key: "customers.view", Group: "Selling", Label: "View customers and tabs"},
        {Key: "customers.manage", Group: "Selling", Label: "Manage customers, credit and tabs"},
        {Key: "payments.manual", Group: "Payments", Label: "Enter manual M-Pesa receipt codes"},
        {Key: "payments.override_price", Group: "Payments", Label: "Override line item prices"},
        {Key: "payments.apply_discount", Group: "Payments", Label: "Apply order discounts"},
        {Key: "loyalty.redeem", Group: "Payments", Label: "Redeem loyalty points as payment"},
        {Key: "credit.manage", Group: "Payments", Label: "Top up and take store credit payments"},
        {Key: "products.view", Group: "Catalog", Label: "View products and stock"},
        {Key: "products.manage", Group: "Catalog", Label: "Manage products, categories, photos, CSV import"},
        {Key: "suppliers.view", Group: "Catalog", Label: "View suppliers and purchase orders"},
        {Key: "suppliers.manage", Group: "Catalog", Label: "Manage suppliers, receive stock, stock takes"},
        {Key: "reports.view", Group: "Insights", Label: "View sales reports"},
        {Key: "shifts.manage", Group: "Shifts", Label: "Open and close shifts"},
        {Key: "design.view", Group: "Production", Label: "View design board"},
        {Key: "design.manage", Group: "Production", Label: "Manage design jobs and attachments"},
        {Key: "users.manage", Group: "Administration", Label: "Manage users"},
        {Key: "roles.manage", Group: "Administration", Label: "Manage roles and dashboards"},
        {Key: "settings.manage", Group: "Administration", Label: "Manage settings"},
        {Key: "audit.view", Group: "Administration", Label: "View audit log"},
        {Key: "printer.test", Group: "Administration", Label: "Test receipt printer"},
}

// PermissionGroups returns group -> keys, for UI rendering.
func PermissionGroups() (order []string, groups map[string][]PermissionDef) {
        groups = map[string][]PermissionDef{}
        for _, p := range PermissionCatalog {
                if _, ok := groups[p.Group]; !ok {
                        order = append(order, p.Group)
                }
                groups[p.Group] = append(groups[p.Group], p)
        }
        return order, groups
}

func ValidPermission(key string) bool {
        for _, p := range PermissionCatalog {
                if p.Key == key {
                        return true
                }
        }
        return false
}

// AllPermissions returns every key (used by the seeded Admin role).
func AllPermissions() []string {
        out := make([]string, 0, len(PermissionCatalog))
        for _, p := range PermissionCatalog {
                out = append(out, p.Key)
        }
        return out
}

// SeededRolePermissions defines the three system default roles.
var SeededRolePermissions = map[string][]string{
        "Admin":    AllPermissions(),
        "Cashier":  {"pos.sell", "pos.void", "orders.view", "payments.manual", "shifts.manage", "customers.view"},
        "Designer": {"design.view", "design.manage", "products.view", "orders.view"},
}
