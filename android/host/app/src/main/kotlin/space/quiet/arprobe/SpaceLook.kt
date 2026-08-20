package space.quiet.arprobe

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.LayerDrawable
import android.graphics.drawable.RippleDrawable
import android.content.res.ColorStateList
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import kotlin.random.Random

/**
 * The desktop lock screen's look, spoken in Views.
 *
 * The palette and the proportions are lifted from clients/lockgate/
 * lockscreen.html on purpose — a person pairing a phone has just looked at
 * that screen on their computer, and arriving somewhere that feels like a
 * different product is a small distrust at the worst moment. Self-contained
 * like its source: no XML, no assets, no fonts beyond the system's.
 *
 * WHAT IS DELIBERATELY NOT PORTED: the drifting starfield and the orbiting
 * planets. The sky here is painted once and holds still — the project's
 * photosensitivity floor forbids pulsing, and on a phone an infinite
 * animation behind a passphrase field is battery spent on nothing. A still
 * sky reads as the same place.
 */
internal object SpaceLook {
    const val BG = 0xFF07070C.toInt()
    const val FG = 0xFFE8E6F0.toInt()
    const val DIM = 0xFF8B88A0.toInt()
    const val MUTED = 0xFF5F5C75.toInt()
    const val ACC = 0xFFD39AE7.toInt()
    const val STAR = 0xFFDFE8FF.toInt()
    const val BAD = 0xFFFF9B9B.toInt()
    const val LINE = 0x14FFFFFF // white at 8% — the hairline everything shares

    private fun dp(ctx: Context, v: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v, ctx.resources.displayMetrics).toInt()

    /** One paint, no motion: the sky the desktop drifts, held still. */
    private class SkyView(ctx: Context) : View(ctx) {
        private val paint = Paint(Paint.ANTI_ALIAS_FLAG)
        override fun onDraw(canvas: Canvas) {
            canvas.drawColor(BG)
            // Seeded, so the sky is the same sky on every launch — stars that
            // rearrange themselves read as a glitch, not a universe.
            val rnd = Random(7)
            val w = width.toFloat()
            val h = height.toFloat()
            if (w <= 0f || h <= 0f) return
            repeat(90) {
                val x = rnd.nextFloat() * w
                val y = rnd.nextFloat() * h
                val r = 0.6f + rnd.nextFloat() * 1.3f
                paint.color = when (rnd.nextInt(4)) {
                    0 -> 0x66CFD8FF
                    1 -> 0x55E8D8FF
                    else -> 0x77FFFFFF
                }
                canvas.drawCircle(x, y, r, paint)
            }
            // Two faint orbits, off-centre like the desktop's — geometry as
            // decoration, quiet enough to miss.
            paint.style = Paint.Style.STROKE
            paint.strokeWidth = 1f
            paint.color = 0x0FFFFFFF
            canvas.drawCircle(w * 0.5f, h * 0.42f, w * 0.55f, paint)
            paint.color = 0x0AFFFFFF
            canvas.drawCircle(w * 0.5f, h * 0.42f, w * 0.92f, paint)
            paint.style = Paint.Style.FILL
        }
    }

    /** The whole screen: still sky behind, one scrollable centered card. */
    fun screen(ctx: Context, card: View): View {
        val scroller = ScrollView(ctx).apply {
            isFillViewport = true
            isVerticalScrollBarEnabled = false
            addView(FrameLayout(ctx).apply {
                addView(card, FrameLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.WRAP_CONTENT,
                    Gravity.CENTER,
                ).apply {
                    val m = dp(ctx, 18f)
                    setMargins(m, dp(ctx, 28f), m, dp(ctx, 28f))
                })
            }, FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
                Gravity.CENTER,
            ))
        }
        return FrameLayout(ctx).apply {
            addView(SkyView(ctx), FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
            addView(scroller, FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        }
    }

    /** SPACE GLASS, opaque edition: no live blur below API 31, so the pane
     *  carries its depth in the gradient and the top light-catch instead. */
    fun card(ctx: Context): LinearLayout = LinearLayout(ctx).apply {
        orientation = LinearLayout.VERTICAL
        gravity = Gravity.CENTER_HORIZONTAL
        val p = dp(ctx, 26f)
        setPadding(p, dp(ctx, 34f), p, p)
        background = LayerDrawable(arrayOf(
            GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM,
                intArrayOf(0xF31C1E2E.toInt(), 0xF610111C.toInt())).apply {
                cornerRadius = dp(ctx, 26f).toFloat()
                setStroke(dp(ctx, 1f), LINE)
            },
            // the inset highlight: a hairline of light along the top edge
            GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM,
                intArrayOf(0x17FFFFFF, 0x00000000)).apply {
                cornerRadius = dp(ctx, 26f).toFloat()
            },
        ))
    }

    fun title(ctx: Context, text: String) = TextView(ctx).apply {
        setText(text)
        setTextColor(FG)
        textSize = 19f
        typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        gravity = Gravity.CENTER
        letterSpacing = 0.01f
    }

    fun sub(ctx: Context, text: String) = TextView(ctx).apply {
        setText(text)
        setTextColor(DIM)
        textSize = 14f
        gravity = Gravity.CENTER
        setLineSpacing(0f, 1.25f)
        setPadding(0, dp(ctx, 6f), 0, 0)
    }

    /** The quiet left-aligned label a section starts with. */
    fun section(ctx: Context, text: String) = TextView(ctx).apply {
        setText(text)
        setTextColor(MUTED)
        textSize = 12.5f
        gravity = Gravity.START
        letterSpacing = 0.02f
        setPadding(dp(ctx, 2f), dp(ctx, 26f), 0, dp(ctx, 8f))
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
    }

    /** The message line under a form — turns red without moving anything. */
    fun note(ctx: Context, text: String) = TextView(ctx).apply {
        setText(text)
        setTextColor(DIM)
        textSize = 12.5f
        gravity = Gravity.CENTER
        setLineSpacing(0f, 1.3f)
        setPadding(dp(ctx, 4f), dp(ctx, 12f), dp(ctx, 4f), dp(ctx, 4f))
    }

    fun input(ctx: Context, hint: String) = EditText(ctx).apply {
        setHint(hint)
        setTextColor(FG)
        setHintTextColor(MUTED)
        textSize = 16f
        backgroundTintList = null
        background = GradientDrawable().apply {
            setColor(0x52000000)
            cornerRadius = dp(ctx, 16f).toFloat()
            setStroke(dp(ctx, 1f), LINE)
        }
        val p = dp(ctx, 16f)
        setPadding(p, dp(ctx, 14f), p, dp(ctx, 14f))
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(ctx, 10f)
        }
    }

    private fun glass(ctx: Context, radius: Float, fillTop: Int, fillBottom: Int): GradientDrawable =
        GradientDrawable(GradientDrawable.Orientation.TOP_BOTTOM, intArrayOf(fillTop, fillBottom)).apply {
            cornerRadius = dp(ctx, radius).toFloat()
            setStroke(dp(ctx, 1f), LINE)
        }

    private fun pressable(ctx: Context, radius: Float, base: GradientDrawable): RippleDrawable =
        RippleDrawable(
            ColorStateList.valueOf(0x2ED39AE7),
            base,
            GradientDrawable().apply {
                setColor(Color.WHITE)
                cornerRadius = dp(ctx, radius).toFloat()
            },
        )

    /** The one inviting button on a card. */
    fun primary(ctx: Context, label: String) = Button(ctx).apply {
        text = label
        isAllCaps = false
        setTextColor(FG)
        textSize = 15f
        typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        stateListAnimator = null
        background = pressable(ctx, 18f,
            glass(ctx, 18f, 0x33D39AE7, 0x1AD39AE7).apply { setStroke(dp(ctx, 1f), 0x59D39AE7) })
        minHeight = dp(ctx, 52f)
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(ctx, 14f)
        }
    }

    /** A quieter verb beside the primary one. */
    fun plain(ctx: Context, label: String) = Button(ctx).apply {
        text = label
        isAllCaps = false
        setTextColor(FG)
        textSize = 14f
        stateListAnimator = null
        background = pressable(ctx, 18f, glass(ctx, 18f, 0x12FFFFFF, 0x06FFFFFF))
        minHeight = dp(ctx, 48f)
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(ctx, 12f)
        }
    }

    /** The underlined footnote door ("use a long passphrase instead"). */
    fun ghost(ctx: Context, label: String) = Button(ctx).apply {
        text = label
        isAllCaps = false
        setTextColor(DIM)
        textSize = 12.5f
        paint.isUnderlineText = true
        stateListAnimator = null
        background = pressable(ctx, 12f, GradientDrawable().apply {
            setColor(Color.TRANSPARENT)
            cornerRadius = dp(ctx, 12f).toFloat()
        })
        minHeight = dp(ctx, 40f)
        layoutParams = LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(ctx, 10f)
            gravity = Gravity.CENTER_HORIZONTAL
        }
    }

    /** A 64dp glass key. The desktop's floor, kept: big enough to hit
     *  without looking. */
    fun key(ctx: Context, label: String) = Button(ctx).apply {
        text = label
        isAllCaps = false
        setTextColor(if (label == "C" || label == "<") DIM else FG)
        textSize = if (label == "C" || label == "<") 16f else 24f
        typeface = Typeface.create("sans-serif", Typeface.NORMAL)
        stateListAnimator = null
        background = pressable(ctx, 18f,
            if (label == "C" || label == "<") glass(ctx, 18f, 0x00000000, 0x00000000)
                .apply { setStroke(0, 0) }
            else glass(ctx, 18f, 0x12FFFFFF, 0x06FFFFFF))
    }

    /** The dot row: filled stars for typed digits. */
    fun dots(ctx: Context) = TextView(ctx).apply {
        setTextColor(STAR)
        textSize = 26f
        gravity = Gravity.CENTER
        letterSpacing = 0.28f
        setPadding(0, dp(ctx, 22f), 0, dp(ctx, 18f))
        setShadowLayer(12f, 0f, 0f, 0x8CDFE8FF.toInt())
    }

    fun keyRowParams(ctx: Context): LinearLayout.LayoutParams =
        LinearLayout.LayoutParams(0, dp(ctx, 64f), 1f).apply {
            val m = dp(ctx, 6f)
            setMargins(m, m, m, m)
        }
}
