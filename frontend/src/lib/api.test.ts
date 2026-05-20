// Verifies the request interceptor stamps X-Requested-With when the document
// is loaded inside an iframe (window.self !== window.top).

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import axios, { AxiosHeaders } from 'axios';
import { attachInterceptors } from './api';

describe('api interceptors', () => {
  const originalTop = window.top;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    Object.defineProperty(window, 'top', {
      value: originalTop,
      configurable: true,
    });
  });

  function fakeIframe(): void {
    // window.self !== window.top → embedded.
    Object.defineProperty(window, 'top', {
      value: { fake: true } as unknown as Window,
      configurable: true,
    });
  }

  it('sets X-Requested-With=moses-iframe when embedded', async () => {
    fakeIframe();
    const instance = axios.create();
    attachInterceptors(instance);

    // Resolve the request synchronously via the adapter so we observe the
    // headers the interceptor produced without making a network call.
    instance.defaults.adapter = vi.fn(async (config) => ({
      data: {},
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }));

    const response = await instance.get('/anything');
    const headers = response.config.headers;
    const value =
      headers instanceof AxiosHeaders
        ? headers.get('X-Requested-With')
        : (headers as Record<string, string>)['X-Requested-With'];
    expect(value).toBe('moses-iframe');
  });

  it('does NOT set X-Requested-With outside an iframe', async () => {
    // window.self === window.top in jsdom by default.
    const instance = axios.create();
    attachInterceptors(instance);
    instance.defaults.adapter = vi.fn(async (config) => ({
      data: {},
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }));

    const response = await instance.get('/anything');
    const headers = response.config.headers;
    const value =
      headers instanceof AxiosHeaders
        ? headers.get('X-Requested-With')
        : (headers as Record<string, string>)['X-Requested-With'];
    expect(value).toBeFalsy();
  });

  it('normalizes errors into {status, code, message}', async () => {
    const instance = axios.create();
    attachInterceptors(instance);
    instance.defaults.adapter = vi.fn(async () => {
      throw Object.assign(new Error('boom'), {
        isAxiosError: true,
        response: {
          status: 418,
          data: { code: 'teapot', message: 'I am a teapot' },
          headers: {},
          statusText: 'Teapot',
          config: {} as never,
        },
      });
    });

    await expect(instance.get('/x')).rejects.toMatchObject({
      status: 418,
      code: 'teapot',
      message: 'I am a teapot',
    });
  });
});
