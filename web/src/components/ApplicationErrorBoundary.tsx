import { Component, type PropsWithChildren } from 'react'
import { Button } from './ui'

export class ApplicationErrorBoundary extends Component<PropsWithChildren, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  render() {
    if (!this.state.failed) return this.props.children

    return (
      <main role="alert" className="app-shell grid place-content-center gap-4 bg-background p-6 text-foreground">
        <h1 className="text-xl">Atlas n’a pas pu afficher cette page</h1>
        <p>Vos sessions sont conservées.</p>
        <Button variant="primary" onClick={() => window.location.reload()}>Recharger la page</Button>
      </main>
    )
  }
}
