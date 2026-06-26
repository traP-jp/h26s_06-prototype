import * as THREE from "three";
import {
  BRANCH_JITTER_MAX,
  BRANCH_JITTER_MIN,
  DRIFT_BASE_AMPLITUDE,
  DRIFT_DEPTH_AMPLITUDE,
  GOLDEN_ANGLE,
  GRAND_ROOT_ID,
  PALETTE,
  ROOT_CLUSTER_PADDING,
  ROOT_DIRECTION_JITTER,
  ROOT_MIN_DISTANCE,
} from "./constants.js";

const defaultZ = new THREE.Vector3(0, 0, 1);

// ChannelGraph は API から届いたチャンネル木を Three.js のノード配置へ変換します。
export class ChannelGraph {
    constructor(channels) {
        this.nodes = [];
        this.nodeMap = new Map();
        this.extent = 50;
        this.root = null;

        const source = new Map(
            Object.values(channels).map((channel) => [channel.id, channel]),
        );
        const ordered = [];
        const seen = new Set();

        const visit = (id) => {
            const channel = source.get(id);
            if (!channel || seen.has(id)) return;
            seen.add(id);
            ordered.push(channel);
            for (const childId of channel.children ?? []) {
                visit(childId);
            }
        };

        visit(GRAND_ROOT_ID);
        for (const channel of source.values()) {
            visit(channel.id);
        }

        for (const channel of ordered) {
            const node = {
                index: this.nodes.length,
                id: channel.id,
                name: channel.name,
                parentId: channel.parentId || "",
                childIds: channel.children ?? [],
                children: [],
                islandId: channel.islandId ?? -1,
                depth: channel.depth ?? 0,
                currentScore: 0,
                targetScore: 0,
                position: new THREE.Vector3(),
                visualPosition: new THREE.Vector3(),
                color: colorForChannel(channel),
                baseScale:
                    channel.id === GRAND_ROOT_ID
                        ? 1.7
                        : Math.max(0.42, 1.35 - (channel.depth ?? 0) * 0.16),
                layoutRadius: 0,
                driftPhase: Math.random() * Math.PI * 2,
                driftSpeed: 0.34 + Math.random() * 0.22,
                driftAmplitude:
                    channel.id === GRAND_ROOT_ID
                        ? 0
                        : DRIFT_BASE_AMPLITUDE +
                          Math.min(channel.depth ?? 0, 8) *
                              DRIFT_DEPTH_AMPLITUDE,
                driftAxisA: randomUnitVector(),
                driftAxisB: randomUnitVector(),
            };
            node.driftAxisB
                .crossVectors(node.driftAxisA, node.driftAxisB)
                .normalize();
            if (node.driftAxisB.lengthSq() < 0.001) {
                node.driftAxisB
                    .crossVectors(node.driftAxisA, defaultZ)
                    .normalize();
            }
            this.nodes.push(node);
            this.nodeMap.set(node.id, node);
            if (node.id === GRAND_ROOT_ID) this.root = node;
        }

        for (const node of this.nodes) {
            node.children = node.childIds
                .map((id) => this.nodeMap.get(id))
                .filter(Boolean);
        }

        if (!this.root && this.nodes.length > 0) {
            this.root = this.nodes[0];
        }
        if (this.root) {
            this.computeLayoutRadii();
            this.placeNodes();
            this.updateVisualPositions(performance.now());
        }
    }

    computeLayoutRadii() {
        const compute = (node) => {
            if (node.children.length === 0) {
                node.layoutRadius = node.baseScale;
                return node.layoutRadius;
            }

            const childDistance =
                branchDistanceForDepth(node.depth) * BRANCH_JITTER_MAX;
            node.layoutRadius = Math.max(
                node.baseScale,
                ...node.children.map(
                    (child) => childDistance + compute(child) + child.baseScale,
                ),
            );
            return node.layoutRadius;
        };

        compute(this.root);
    }

    placeNodes() {
        const rootDirections =
            this.root.children.length > 0
                ? rotatedFibonacciDirections(
                      this.root.children.length,
                      ROOT_DIRECTION_JITTER,
                  )
                : [];
        const rootDistance =
            this.root.children.length > 0
                ? this.rootDistanceFor(rootDirections)
                : ROOT_MIN_DISTANCE;

        const place = (node, position, outwardDir) => {
            node.position.copy(position);
            node.visualPosition.copy(position);
            this.extent = Math.max(this.extent, position.length());
            if (node.children.length === 0) return;

            const childCount = node.children.length;
            const baseDistance = branchDistanceForDepth(node.depth);
            const maxSpreadAngle = spreadAngleForDepth(node.depth);
            const offset = Math.random() * Math.PI * 2;
            const quaternion = new THREE.Quaternion().setFromUnitVectors(
                defaultZ,
                outwardDir.clone().normalize(),
            );

            for (let index = 0; index < childCount; index += 1) {
                const child = node.children[index];
                let direction;

                if (node.id === GRAND_ROOT_ID && rootDirections[index]) {
                    direction = rootDirections[index].clone();
                } else {
                    const spread =
                        childCount === 1 ? 0 : index / (childCount - 1);
                    const z = THREE.MathUtils.clamp(
                        1 - spread * (1 - Math.cos(maxSpreadAngle)),
                        -1,
                        1,
                    );
                    const radius = Math.sqrt(Math.max(0, 1 - z * z));
                    const theta = index * GOLDEN_ANGLE + offset;
                    direction = new THREE.Vector3(
                        Math.cos(theta) * radius,
                        Math.sin(theta) * radius,
                        z,
                    );
                    direction.applyQuaternion(quaternion).normalize();
                }

                const jitter =
                    node.id === GRAND_ROOT_ID
                        ? 1
                        : BRANCH_JITTER_MIN +
                          Math.random() *
                              (BRANCH_JITTER_MAX - BRANCH_JITTER_MIN);
                const childPosition = position
                    .clone()
                    .add(direction.multiplyScalar(baseDistance * jitter));
                place(
                    child,
                    childPosition,
                    childPosition.clone().sub(position).normalize(),
                );
            }
        };

        place(
            this.root,
            new THREE.Vector3(0, 0, 0),
            new THREE.Vector3(0, 1, 0),
        );
    }

    rootDistanceFor(directions) {
        let requiredDistance = ROOT_MIN_DISTANCE;

        for (let i = 0; i < this.root.children.length; i += 1) {
            for (let j = i + 1; j < this.root.children.length; j += 1) {
                const chord = Math.max(
                    0.08,
                    directions[i].distanceTo(directions[j]),
                );
                const required =
                    (this.root.children[i].layoutRadius +
                        this.root.children[j].layoutRadius +
                        ROOT_CLUSTER_PADDING) /
                    chord;
                requiredDistance = Math.max(requiredDistance, required);
            }
        }

        return requiredDistance;
    }

    get(id) {
        return this.nodeMap.get(id);
    }

    heatAncestors(node, amount) {
        let current = node;
        let heat = amount;
        while (current) {
            current.currentScore = Math.min(100, current.currentScore + heat);
            current.targetScore = Math.max(
                current.targetScore,
                current.currentScore * 0.62,
            );
            if (current.id === GRAND_ROOT_ID) break;
            current = this.nodeMap.get(current.parentId);
            heat *= 0.45;
        }
    }

    heatNode(node, amount) {
        node.currentScore = Math.min(100, node.currentScore + amount);
        node.targetScore = Math.max(node.targetScore, node.currentScore * 0.7);
    }

    sync(deltas) {
        for (const [id, score] of Object.entries(deltas ?? {})) {
            const node = this.nodeMap.get(id);
            if (node) {
                node.targetScore = score;
            }
        }
    }

    update(delta, now) {
        const decay = Math.exp(-delta * 0.78);
        const targetDecay = Math.exp(-delta * 0.48);
        for (const node of this.nodes) {
            node.currentScore *= decay;
            node.targetScore *= targetDecay;
            node.currentScore += (node.targetScore - node.currentScore) * 0.07;
            if (node.currentScore < 0.015) node.currentScore = 0;
        }
        this.updateVisualPositions(now);
    }

    updateVisualPositions(now) {
        const time = now * 0.001;
        for (const node of this.nodes) {
            const heatLift = THREE.MathUtils.clamp(
                node.currentScore / 160,
                0,
                0.7,
            );
            const amplitude = node.driftAmplitude * (1 + heatLift);
            const waveA =
                Math.sin(time * node.driftSpeed + node.driftPhase) * amplitude;
            const waveB =
                Math.cos(
                    time * node.driftSpeed * 0.73 + node.driftPhase * 1.31,
                ) *
                amplitude *
                0.58;
            node.visualPosition
                .copy(node.position)
                .addScaledVector(node.driftAxisA, waveA)
                .addScaledVector(node.driftAxisB, waveB);
        }
    }

    topNodes(limit) {
        return this.nodes
            .filter((node) => node.id !== GRAND_ROOT_ID)
            .sort((a, b) => b.currentScore - a.currentScore)
            .slice(0, limit)
            .map((node) => ({
                id: node.id,
                name: node.name,
                score: node.currentScore,
                color: `#${node.color.getHexString()}`,
            }));
    }
}

function colorForChannel(channel) {
    if (channel.id === GRAND_ROOT_ID) {
        return new THREE.Color("#d6dde8");
    }
    const color = new THREE.Color(
        PALETTE[Math.max(0, channel.islandId ?? 0) % PALETTE.length],
    );
    const depthFade = THREE.MathUtils.clamp(
        1 - ((channel.depth ?? 0) - 1) * 0.055,
        0.58,
        1,
    );
    return color.lerp(new THREE.Color("#dbe8ff"), 1 - depthFade);
}

function branchDistanceForDepth(depth) {
    return 80 * Math.pow(0.3, depth - 1);
}

function spreadAngleForDepth(depth) {
    if (depth === 0) return Math.PI * 0.96;
    return Math.PI * 0.76 * Math.pow(0.82, depth - 1);
}

function randomUnitVector() {
    const z = THREE.MathUtils.randFloatSpread(2);
    const radius = Math.sqrt(Math.max(0, 1 - z * z));
    const theta = Math.random() * Math.PI * 2;
    return new THREE.Vector3(
        Math.cos(theta) * radius,
        Math.sin(theta) * radius,
        z,
    );
}

function rotatedFibonacciDirections(count, jitter = 0) {
    if (count <= 0) return [];

    const directions = [];
    const rotation = new THREE.Quaternion().setFromEuler(
        new THREE.Euler(
            Math.random() * Math.PI,
            Math.random() * Math.PI,
            Math.random() * Math.PI,
        ),
    );

    for (let index = 0; index < count; index += 1) {
        const y = 1 - ((index + 0.5) / count) * 2;
        const radius = Math.sqrt(Math.max(0, 1 - y * y));
        const theta = index * GOLDEN_ANGLE;
        const direction = new THREE.Vector3(
            Math.cos(theta) * radius,
            y,
            Math.sin(theta) * radius,
        );

        if (jitter > 0) {
            direction
                .addScaledVector(randomUnitVector(), Math.random() * jitter)
                .normalize();
        }

        direction.applyQuaternion(rotation).normalize();
        directions.push(direction);
    }

    return directions;
}
