import { usePath, parseRoute } from './router'
import { Board } from './components/Board'
import { History } from './components/History'
import { RunDetail } from './components/RunDetail'

export default function App() {
  const route = parseRoute(usePath())

  return (
    <div className="mx-auto min-h-full max-w-7xl px-4 py-6">
      <header className="mb-6 flex items-baseline gap-3">
        <a href="#/" className="text-lg font-bold tracking-tight text-slate-100 hover:text-white">
          redrill
        </a>
        <span className="text-xs text-slate-500">last proven, not last backed up</span>
      </header>
      <main>
        {route.name === 'board' && <Board />}
        {route.name === 'history' && <History drill={route.drill} />}
        {route.name === 'run' && <RunDetail id={route.id} />}
      </main>
    </div>
  )
}
