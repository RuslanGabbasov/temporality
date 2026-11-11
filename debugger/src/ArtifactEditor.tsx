import { useEffect, useRef } from 'react'
import {
  MDXEditor,
  type MDXEditorMethods,
  headingsPlugin,
  listsPlugin,
  linkPlugin,
  linkDialogPlugin,
  tablePlugin,
  thematicBreakPlugin,
  markdownShortcutPlugin,
  codeBlockPlugin,
  toolbarPlugin,
  diffSourcePlugin,
  UndoRedo,
  BlockTypeSelect,
  BoldItalicUnderlineToggles,
  CodeToggle,
  InsertTable,
  InsertThematicBreak,
  ListsToggle,
  CreateLink,
  InsertCodeBlock,
  ConditionalContents,
  ChangeCodeMirrorLanguage,
  DiffSourceToggleWrapper,
  codeMirrorPlugin,
} from '@mdxeditor/editor'
import '@mdxeditor/editor/style.css'
import { useTheme } from './theme'

// The plugin set is shared with the fidelity test: the stored markdown must
// survive the exact round-trip the UI performs (presentations fences,
// mermaid, tables, hard breaks). Exported, not inlined, for that reason.
// withToolbar=false strips the toolbar only — jsdom cannot mount the
// CodeMirror-backed toolbar items, and the fidelity test cares about the
// document round-trip, not the chrome.
export function artifactEditorPlugins(withToolbar = true) {
  const plugins = [
    headingsPlugin(),
    listsPlugin(),
    linkPlugin(),
    linkDialogPlugin(),
    tablePlugin(),
    thematicBreakPlugin(),
    markdownShortcutPlugin(),
    codeBlockPlugin({ defaultCodeBlockLanguage: 'md' }),
    codeMirrorPlugin({ codeBlockLanguages: { md: 'markdown', text: 'text', js: 'JavaScript', ts: 'TypeScript', go: 'Go', sql: 'SQL', bash: 'Bash', json: 'JSON', yaml: 'YAML', mermaid: 'mermaid', presentation: 'presentation' } }),
    diffSourcePlugin({ viewMode: 'rich-text', diffMarkdown: '' }),
  ]
  if (withToolbar) {
    plugins.push(
      toolbarPlugin({
        // ChangeCodeMirrorLanguage renders ONLY when a code block is in
        // focus (ConditionalContents) — outside that guard it read a null
        // node and crashed the whole editor (the «rootNode of null» page
        // killer on first Edit click).
        toolbarContents: () => (
          <DiffSourceToggleWrapper>
            <UndoRedo />
            <BlockTypeSelect />
            <BoldItalicUnderlineToggles />
            <CodeToggle />
            <InsertCodeBlock />
            <ListsToggle />
            <CreateLink />
            <InsertTable />
            <InsertThematicBreak />
            <ConditionalContents
              options={[{ when: (editor) => editor?.editorType === 'codeblock', contents: () => <ChangeCodeMirrorLanguage /> }]}
            />
          </DiffSourceToggleWrapper>
        ),
      }),
    )
  }
  return plugins
}

/** The artifact document editor: a full-pane WYSIWYG over the raw markdown
 *  (docs/artifact-lifecycle.md — the body is a version's payload and stays
 *  byte-honest markdown). Round-trip fidelity is the hard requirement —
 *  skills and agents read the stored text — so the editor is markdown-native
 *  (MDXEditor), code fences (```presentation / ```mermaid / any language)
 *  stay verbatim CodeMirror blocks, and the markdown source view is one
 *  toggle away for anything exotic. */
export function ArtifactEditor({ markdown, onChange, onReady, readOnly }: {
  markdown: string
  onChange: (value: string) => void
  onReady?: (methods: { getMarkdown: () => string }) => void
  readOnly?: boolean
}) {
  const ref = useRef<MDXEditorMethods | null>(null)
  // MDXEditor's palette is driven by its theme class: the scales (slate/blue/
  // amber/…) flip between the light :root values and the .dark-theme values.
  // It must follow the APP theme, not a hardcoded dark — a hardcoded dark
 //  left half the control invisible on the light theme.
  const { resolved } = useTheme()
  // The editor is uncontrolled: markdown flows in once per mount (a key on
  // the parent side remounts it per version switch), edits flow out.
  useEffect(() => { if (onReady && ref.current) onReady({ getMarkdown: () => ref.current!.getMarkdown() }) }, [onReady])
  return (
    <div className="artifact-editor">
      <MDXEditor
        ref={ref}
        className={resolved === 'light' ? 'light-theme' : 'dark-theme'}
        markdown={markdown}
        onChange={onChange}
        readOnly={readOnly}
        plugins={artifactEditorPlugins()}
      />
    </div>
  )
}
