import { Component } from 'react'

// React only supports catching render-time errors via a class component's
// getDerivedStateFromError/componentDidCatch — there's no hook equivalent.
// Without this, an uncaught render error anywhere in the tree previously
// unmounted the whole app to a blank window with no way back short of
// relaunching. Reuses the .stopped-state/-title/-hint classes App.jsx
// already defines for its "server has stopped" full-screen state, since
// this is the same kind of "the app can't continue, here's what to do"
// screen.
export default class ErrorBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidCatch(error, info) {
    console.error('Uncaught render error:', error, info.componentStack)
  }

  render() {
    if (this.state.error) {
      return (
        <div className="app">
          <div className="stopped-state">
            <p className="stopped-title">Something went wrong.</p>
            <p className="stopped-hint">{this.state.error.message}</p>
            <button type="button" className="btn-secondary" onClick={() => window.location.reload()}>
              Reload
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}
