// Source files under src/ use vite-style extensionless relative imports
// (e.g. `import x from '../utils/modelPreference'`). Plain node's ESM resolver
// refuses those, so this hook retries the specifier with a known extension.
// It is only used by the node-based tests in this directory.

export async function resolve(specifier, context, nextResolve) {
  try {
    return await nextResolve(specifier, context);
  } catch (error) {
    if (specifier.startsWith('.') || specifier.startsWith('/')) {
      for (const candidate of ['.js', '/index.js']) {
        try {
          return await nextResolve(specifier + candidate, context);
        } catch {
          // fall through to the next candidate
        }
      }
    }
    throw error;
  }
}
