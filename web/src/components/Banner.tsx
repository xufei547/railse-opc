import type { ReactNode } from 'react'

export function Banner({ tone, children }: { tone: 'warn' | 'info' | 'ok'; children: ReactNode }) {
  return (
    <div className={`banner ${tone}`} role="status">
      {children}
    </div>
  )
}
