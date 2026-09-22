// Same fetch-wrapper discipline as the panel's api.js (docs/demos.md §Control
// dashboard UI): same-origin relative paths, credentials: 'include',
// X-CSRF-Token on every mutating request, `{ "error": { "message" } }`
// unwrapping, `.status` on thrown errors.

let csrfToken = null

// Called once per session with the token from GET /api/me; held in memory
// only — never persisted.
export const setCsrfToken = (token) => {
  csrfToken = token
}

const toApiError = (status, payload, fallback) => {
  const error = new Error(payload?.error?.message ?? fallback)
  error.status = status
  return error
}

const parseJson = (text) => {
  if (!text) return null
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

const request = async (path, { method = 'GET', body } = {}) => {
  const headers = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && csrfToken) headers['X-CSRF-Token'] = csrfToken

  const init = {
    method,
    credentials: 'include',
    headers,
  }
  if (body !== undefined) init.body = JSON.stringify(body)

  const res = await fetch(path, init)

  const data = res.status === 204 ? null : parseJson(await res.text())

  if (!res.ok) {
    throw toApiError(res.status, data, `Request failed with status ${res.status}`)
  }
  return data
}

export const api = {
  get: (path) => request(path),
  post: (path, body) => request(path, { method: 'POST', body }),
  patch: (path, body) => request(path, { method: 'PATCH', body }),
  del: (path) => request(path, { method: 'DELETE' }),
}

// Multipart zip deploy — XMLHttpRequest for upload progress events (docs/
// demos.md §Control dashboard UI); onProgress reports 0–100.
export const uploadZip = (name, file, onProgress) =>
  new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/demos/${encodeURIComponent(name)}/deploy`)
    xhr.withCredentials = true
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken)

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && onProgress) {
        onProgress(Math.round((event.loaded / event.total) * 100))
      }
    }

    xhr.onload = () => {
      const data = parseJson(xhr.responseText)
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data)
        return
      }
      reject(toApiError(xhr.status, data, `Upload failed with status ${xhr.status}`))
    }

    xhr.onerror = () => {
      reject(toApiError(0, null, 'Upload failed — network error'))
    }

    const form = new FormData()
    form.append('zip', file)
    xhr.send(form)
  })
