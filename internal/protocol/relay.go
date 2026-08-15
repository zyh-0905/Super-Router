package protocol

// 中转站（渠道）类型。主流部署只有两类：
//   - newapi：new-api / one-api 系（余额接口 /api/user/self）
//   - sub2api：Sub2API（余额接口 /api/v1/auth/me）
//   - custom：自定义（余额接口手动填写）
const (
	RelayTypeNewAPI  = "newapi"
	RelayTypeSub2API = "sub2api"
	RelayTypeCustom  = "custom"
)

// ValidRelayType 校验中转站类型取值
func ValidRelayType(t string) bool {
	return t == "" || t == RelayTypeNewAPI || t == RelayTypeSub2API || t == RelayTypeCustom
}

// RelayTypeLabel 返回类型的中文显示名（未知类型返回空）
func RelayTypeLabel(t string) string {
	switch t {
	case RelayTypeNewAPI:
		return "New API"
	case RelayTypeSub2API:
		return "Sub2API"
	case RelayTypeCustom:
		return "自定义"
	}
	return ""
}

// DefaultBalanceEndpoint 按中转站类型返回默认余额接口路径（相对 base_url）。
// custom 或未知类型返回空串，表示无默认值、需手动配置。
func DefaultBalanceEndpoint(relayType string) string {
	switch relayType {
	case RelayTypeSub2API:
		return "/api/v1/auth/me"
	case RelayTypeNewAPI:
		return "/api/user/self"
	}
	return ""
}
