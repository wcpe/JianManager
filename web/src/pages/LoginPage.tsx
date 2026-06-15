export default function LoginPage() {
  return (
    <div className="flex items-center justify-center h-screen">
      <div className="w-full max-w-sm space-y-4 p-6">
        <h1 className="text-2xl font-bold text-center">JianManager</h1>
        <p className="text-muted-foreground text-center">登录到管理平台</p>
        <div className="space-y-2">
          <input
            type="text"
            placeholder="用户名"
            className="w-full px-3 py-2 border rounded-md bg-background"
          />
          <input
            type="password"
            placeholder="密码"
            className="w-full px-3 py-2 border rounded-md bg-background"
          />
          <button className="w-full px-3 py-2 bg-primary text-primary-foreground rounded-md">
            登录
          </button>
        </div>
      </div>
    </div>
  )
}
