import { useState } from 'react'
import { TOKEN_STORAGE_KEY, authToken } from './api'

// Token lets an operator store the API bearer token in localStorage instead
// of editing proxy config. Saving reloads so every list reloads with auth.
export default function Token() {
  const [value, setValue] = useState(authToken())
  const [saved, setSaved] = useState(false)
  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    const token = value.trim()
    if (token) localStorage.setItem(TOKEN_STORAGE_KEY, token)
    else localStorage.removeItem(TOKEN_STORAGE_KEY)
    setSaved(true)
    window.setTimeout(() => window.location.reload(), 400)
  }
  return <form className="token-form" onSubmit={submit}>
    <input type="password" value={value} onChange={(change) => { setValue(change.target.value); setSaved(false) }} placeholder="API token" aria-label="API token" />
    <button type="submit">{saved ? 'Reloading…' : 'Save token'}</button>
  </form>
}
