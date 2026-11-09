export const zhCN = {
  app: {
    name: 'boxpanel',
    description: 'sing-box 服务端管理面板',
    placeholder: '面板正在开发中。',
    navigation: '主导航',
  },
  pages: {
    home: '概览',
    users: '用户列表',
    userDetail: '用户详情',
    userID: '用户编号',
    exampleUser: '查看示例用户',
  },
} as const

// Also embedded by Go for the standalone subscription page. Keep the marked
// object valid JSON so both renderers share this single source of UI copy.
export const subscriptionText = /* subscription-text:start */ {
  "scan": "扫描二维码或复制订阅地址，导入客户端。",
  "qr": "订阅二维码",
  "singboxImport": "一键导入 sing-box",
  "singbox": "JSON 订阅",
  "clashImport": "一键导入 Clash / mihomo",
  "clash": "YAML 订阅",
  "base64": "通用 base64 URI 订阅",
  "upload": "上传",
  "download": "下载",
  "bytes": "字节",
  "total": "总限额",
  "expiry": "到期",
  "unlimited": "不限",
  "never": "永不到期",
  "compatibility": "sing-box 客户端需 1.14 或更新。通用 URI 不含分流规则，请在客户端内选择「绕过大陆」等规则。",
  "privacy": "请妥善保存订阅链接，不要公开分享；链接失效时请联系管理员。",
  "select": "节点选择",
  "auto": "自动选择"
} /* subscription-text:end */ as const
