import { useState, type FormEvent } from 'react'
import { useLogin, useRegister } from '@/api/auth'

export default function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [isRegister, setIsRegister] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const login = useLogin()
  const register = useRegister()

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setSuccess('')

    if (isRegister) {
      register.mutate(
        { username, password },
        {
          onSuccess: () => {
            setSuccess('注册成功，请登录')
            setIsRegister(false)
          },
          onError: (err: Error & { response?: { data?: { message?: string } } }) => {
            setError(err.response?.data?.message || '注册失败')
          },
        },
      )
    } else {
      login.mutate(
        { username, password },
        {
          onError: (err: Error & { response?: { data?: { message?: string } } }) => {
            setError(err.response?.data?.message || '登录失败')
          },
        },
      )
    }
  }

  return (
    <div className="flex items-center justify-center h-screen">
      <div className="w-full max-w-sm space-y-4 p-6">
        <h1 className="text-2xl font-bold text-center">JianManager</h1>
        <p className="text-muted-foreground text-center">
          {isRegister ? '创建新账号' : '登录到管理平台'}
        </p>

        {error && (
          <div className="p-3 text-sm text-destructive bg-destructive/10 rounded-md">
            {error}
          </div>
        )}
        {success && (
          <div className="p-3 text-sm text-green-600 bg-green-50 rounded-md">
            {success}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-2">
          <input
            type="text"
            placeholder="用户名"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full px-3 py-2 border rounded-md bg-background"
            required
          />
          <input
            type="password"
            placeholder="密码"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full px-3 py-2 border rounded-md bg-background"
            required
            minLength={6}
          />
          <button
            type="submit"
            disabled={login.isPending || register.isPending}
            className="w-full px-3 py-2 bg-primary text-primary-foreground rounded-md disabled:opacity-50"
          >
            {login.isPending || register.isPending
              ? '处理中...'
              : isRegister
                ? '注册'
                : '登录'}
          </button>
        </form>

        <button
          type="button"
          onClick={() => {
            setIsRegister(!isRegister)
            setError('')
            setSuccess('')
          }}
          className="w-full text-sm text-muted-foreground hover:text-foreground"
        >
          {isRegister ? '已有账号？去登录' : '没有账号？去注册'}
        </button>
      </div>
    </div>
  )
}
