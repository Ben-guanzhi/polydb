package contract

// net 场景的「不可用」判定（PD-23，2026-11-10）。
//
// 契约测试对 net 场景的既定纪律是「本机没有该数据库服务 ⇒ soft-skip」
//（spec/behavior.md 的服务缺失容忍）。但**凭证错误**不是"服务缺失"——
// 它是配置错误，若也静默跳过，就会出现"契约测试全绿、实际什么都没测"的
// 假象。
//
// 本波的实际经历：MSSQL 容器的 sa 密码是 PolyDb_Test_2026，而契约测试的
// 默认 DSN 是 `mssql://sa@localhost:1433/master`（**不带密码**）⇒ 服务在
// 监听、但登录失败 ⇒ 场景被当成"服务不可用"跳过 ⇒ 断言（哪怕把期望值改
// 成错的）也照样 PASS——一度让"列类型断言已生效"的结论被证伪。
//
// 故判定分两类：
//   - 拨号类失败（连接被拒/超时/主机不可达）⇒ 服务缺失 ⇒ soft-skip；
//   - 认证类失败（登录被拒/密码错/无权限）⇒ 配置错误 ⇒ **必须报错**。

import (
	"strings"
)

// authFailureMarkers 是各引擎认证失败的特征串（大小写不敏感）。
//
// 覆盖：
//   - MSSQL："Login failed for user 'sa'. (18456)"（go-mssqldb / tiberius）；
//   - PostgreSQL："password authentication failed for user" /
//     "FATAL: password authentication failed"（pgx / libpq）；
//   - MySQL："Access denied for user"（含 MariaDB 的
//     "using password: YES"）；
//   - Oracle："ORA-01017: invalid username/password; logon denied"。
var authFailureMarkers = []string{
	"login failed",
	"password authentication failed",
	"access denied for user",
	"invalid username/password",
	"logon denied",
	"ora-01017",
	"authentication failed",
	"permission denied",
}

// isAuthFailure 报告错误文本是否为**认证/授权**失败（而非服务不可达）。
//
// 只看 message 文本：内核的 dberror 分类里没有 AuthenticationFailed 类
// （dbcore/dberror/dberror.go:121-147 的 patterns 不含 "Login failed"/
// "Access denied" 等），故认证失败落进 default 未分类，无法靠错误码区分。
func isAuthFailure(msg string) bool {
	m := strings.ToLower(msg)
	for _, marker := range authFailureMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}
