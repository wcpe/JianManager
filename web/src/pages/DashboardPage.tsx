export default function DashboardPage() {
  return (
    <div className="flex h-screen">
      <aside className="w-60 border-r p-4">
        <h2 className="font-bold mb-4">JianManager</h2>
        <nav className="space-y-1 text-sm">
          <a href="/" className="block px-3 py-2 rounded-md bg-accent">仪表盘</a>
          <a href="/nodes" className="block px-3 py-2 rounded-md hover:bg-accent">节点</a>
          <a href="/instances" className="block px-3 py-2 rounded-md hover:bg-accent">实例</a>
          <a href="/bots" className="block px-3 py-2 rounded-md hover:bg-accent">Bot</a>
          <hr className="my-2" />
          <a href="/users" className="block px-3 py-2 rounded-md hover:bg-accent">用户</a>
          <a href="/groups" className="block px-3 py-2 rounded-md hover:bg-accent">用户组</a>
        </nav>
      </aside>
      <main className="flex-1 p-6 overflow-auto">
        <h1 className="text-2xl font-bold mb-4">仪表盘</h1>
        <p className="text-muted-foreground">欢迎使用 JianManager 管理平台</p>
      </main>
    </div>
  )
}
