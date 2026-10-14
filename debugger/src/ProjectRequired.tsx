import { Tile } from '@carbon/react'
import { useT } from './i18n'

/**
 * Placeholder for pages that operate on one concrete project (workspace
 * files/chats, agent bindings, knowledge projection, experience timeline):
 * with the global "All projects" filter active (docs/activity-scoping.md §7)
 * they have nothing to anchor on — ask for a project instead of firing
 * request after request with an empty id.
 */
export default function ProjectRequired({ what }: { what?: string }) {
  const t = useT()
  return (
    <div style={{ padding: '2rem', display: 'flex', justifyContent: 'center' }}>
      <Tile style={{ maxWidth: '420px', textAlign: 'center', padding: '2rem' }}>
        <p style={{ fontSize: '0.95rem', color: 'var(--tm-text-2)', margin: 0 }}>
          {what
            ? (t('projects.required_what', { what }) ?? `Select a project to open ${what}.`)
            : (t('projects.required') ?? 'This page works inside one project — pick a project in the header filter.')}
        </p>
        <p style={{ fontSize: '0.8rem', color: 'var(--tm-text-3)', margin: '0.75rem 0 0' }}>
          {t('projects.required_hint') ?? 'The “All projects” filter lists global activity (runs, triggers, operations); project pages need a concrete project.'}
        </p>
      </Tile>
    </div>
  )
}
