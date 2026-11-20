"use client";

import * as React from "react";

import { Card, CardContent } from "@/components/ui/card";
import { ExplorationGraph } from "@/components/exploration-graph";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

export function GraphTab({ taskId }: { taskId: string }) {
 const [nodes, setNodes] = React.useState<TaskNode[]>([]);
 const [edges, setEdges] = React.useState<Edge[]>([]);
 // Signature of the last graph data:Skip setState when polling for the same data.,Avoid unnecessary full rebuild of the entire image(During drag
 // To avoid being interrupted and stymied by a 20s poll.). only take fields that affect rendering.
 const sigRef = React.useRef("");

 React.useEffect(() => {
 let cancelled = false;
 sigRef.current = ""; // Switch Tasks:Force next refresh.
 const load = () => {
 api
 .explorationGraph(taskId)
 .then((g) => {
 if (cancelled) return;
 const ns = g.nodes?? [];
 const es = g.edges?? [];
 const sig = JSON.stringify([
 ns.map((n) => [n.id, n.type, n.state, n.priority, n.payload]),
 es.map((e) => [e.src, e.dst, e.rel]),
 ]);
 if (sig === sigRef.current) return; // No change → Do not rebuild
 sigRef.current = sig;
 setNodes(ns);
 setEdges(es);
 })
 .catch(() => {
 /* keep last good data */
 });
 };
 load();
 const timer = setInterval(load, 20000);
 return () => {
 cancelled = true;
 clearInterval(timer);
 };
 }, [taskId]);

 return (
 <Card>
 <CardContent className="p-0">
 <ExplorationGraph nodes={nodes} edges={edges} className="h-[72vh]" />
 </CardContent>
 </Card>
);
}
