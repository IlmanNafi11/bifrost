import type {
	ProxyPool,
	ProxyPoolIDsRequest,
	ProxyPoolImportRequest,
	ProxyPoolKeysRequest,
	ProxyPoolMutation,
	ProxyPoolProbeRequest,
	ProxyProbeResult,
} from "@/lib/types/proxyPools";
import { baseApi } from "./baseApi";

// baseUrl already ends in /api, so every path here is root-relative to that.
export const proxyPoolsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getProxyPools: builder.query<{ pools: ProxyPool[] }, void>({
			query: () => "/proxy-pools",
			providesTags: ["ProxyPools"],
		}),
		getProxyPool: builder.query<ProxyPool, string>({
			query: (id) => `/proxy-pools/${id}`,
			providesTags: (_result, _error, id) => [{ type: "ProxyPools", id }],
		}),
		createProxyPool: builder.mutation<ProxyPool, ProxyPoolMutation>({
			query: (body) => ({ url: "/proxy-pools", method: "POST", body }),
			invalidatesTags: ["ProxyPools"],
		}),
		updateProxyPool: builder.mutation<ProxyPool, { id: string; data: ProxyPoolMutation }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}`, method: "PUT", body: data }),
			invalidatesTags: (_result, _error, { id }) => ["ProxyPools", { type: "ProxyPools", id }],
		}),
		deleteProxyPool: builder.mutation<{ status: string }, string>({
			query: (id) => ({ url: `/proxy-pools/${id}`, method: "DELETE" }),
			invalidatesTags: ["ProxyPools", "ProviderKeys"],
		}),
		importProxies: builder.mutation<{ imported: number }, { id: string; data: ProxyPoolImportRequest }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/import`, method: "POST", body: data }),
			invalidatesTags: (_result, _error, { id }) => [{ type: "ProxyPools", id }],
		}),
		probeProxies: builder.mutation<{ results: ProxyProbeResult[] }, { id: string; data?: ProxyPoolProbeRequest }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/probe`, method: "POST", body: data ?? {} }),
			invalidatesTags: (_result, _error, { id }) => [{ type: "ProxyPools", id }],
		}),
		disableProxies: builder.mutation<{ updated: number }, { id: string; data: ProxyPoolIDsRequest }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/disable`, method: "POST", body: data }),
			invalidatesTags: (_result, _error, { id }) => [{ type: "ProxyPools", id }],
		}),
		deleteProxies: builder.mutation<{ deleted: number }, { id: string; data: ProxyPoolIDsRequest }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/delete`, method: "POST", body: data }),
			invalidatesTags: (_result, _error, { id }) => [{ type: "ProxyPools", id }],
		}),
		bindKeys: builder.mutation<{ bound: number }, { id: string; data: ProxyPoolKeysRequest }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/bind`, method: "POST", body: data }),
			invalidatesTags: ["ProviderKeys", "ProxyPools"],
		}),
		unbindKeys: builder.mutation<{ unbound: number }, { id: string; data: { key_ids: string[] } }>({
			query: ({ id, data }) => ({ url: `/proxy-pools/${id}/unbind`, method: "POST", body: data }),
			invalidatesTags: ["ProviderKeys", "ProxyPools"],
		}),
	}),
});

export const {
	useGetProxyPoolsQuery,
	useGetProxyPoolQuery,
	useCreateProxyPoolMutation,
	useUpdateProxyPoolMutation,
	useDeleteProxyPoolMutation,
	useImportProxiesMutation,
	useProbeProxiesMutation,
	useDisableProxiesMutation,
	useDeleteProxiesMutation,
	useBindKeysMutation,
	useUnbindKeysMutation,
} = proxyPoolsApi;