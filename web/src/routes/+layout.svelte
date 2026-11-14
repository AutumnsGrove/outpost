<script lang="ts">
	import { setLucideProps } from '@lucide/svelte/context';
	import '../app.css';

	// One stroke weight and colour for every icon; the size comes from app.css so
	// a glyph sits with the type around it instead of at Lucide's 24px default.
	setLucideProps({ strokeWidth: 1.75 });

	let { children } = $props();
</script>

{@render children()}
