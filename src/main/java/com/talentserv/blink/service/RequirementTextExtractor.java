package com.talentserv.blink.service;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.Charset;
import java.nio.charset.CharsetDecoder;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.util.Locale;

import org.apache.pdfbox.Loader;
import org.apache.pdfbox.pdmodel.PDDocument;
import org.apache.pdfbox.pdmodel.encryption.InvalidPasswordException;
import org.apache.pdfbox.text.PDFTextStripper;
import org.apache.poi.hwpf.HWPFDocument;
import org.apache.poi.hwpf.extractor.WordExtractor;
import org.apache.poi.xwpf.extractor.XWPFWordExtractor;
import org.apache.poi.xwpf.usermodel.XWPFDocument;
import org.springframework.stereotype.Service;

/**
 * Reads an uploaded requirement document into plain text so clarify, tickets,
 * and the workspace use the same wording as a paste.
 */
@Service
public class RequirementTextExtractor {

    static final int MAX_CHARS = 60_000;

    public String extract(String fileName, byte[] bytes) {
        if (bytes == null || bytes.length == 0) {
            throw new IllegalArgumentException("The file is empty.");
        }
        String extension = extension(fileName);
        String raw = switch (extension) {
            case "pdf" -> readPdf(bytes);
            case "docx" -> readDocx(bytes);
            case "doc" -> readDoc(bytes);
            case "txt", "md", "markdown" -> readPlain(bytes);
            default -> throw new IllegalArgumentException("Use a PDF, Word, TXT, or Markdown file.");
        };
        String text = normalize(raw);
        if (text.isBlank()) {
            throw new IllegalArgumentException(emptyMessage(extension));
        }
        if (text.length() > MAX_CHARS) {
            text = text.substring(0, MAX_CHARS).stripTrailing()
                    + "\n\n[Truncated. The rest of the document was not included. Shorten the file or paste the important part.]";
        }
        return text;
    }

    private static String readPdf(byte[] bytes) {
        try (PDDocument document = Loader.loadPDF(bytes)) {
            PDFTextStripper stripper = new PDFTextStripper();
            stripper.setSortByPosition(true);
            return stripper.getText(document);
        } catch (InvalidPasswordException ex) {
            throw new IllegalArgumentException("This PDF is password-protected. Remove the password or paste the requirements.");
        } catch (IOException | RuntimeException ex) {
            throw new IllegalArgumentException("Could not read this PDF. Paste the requirements instead.");
        }
    }

    private static String readDocx(byte[] bytes) {
        try (ByteArrayInputStream input = new ByteArrayInputStream(bytes);
                XWPFDocument document = new XWPFDocument(input);
                XWPFWordExtractor extractor = new XWPFWordExtractor(document)) {
            return extractor.getText();
        } catch (IOException | RuntimeException ex) {
            throw new IllegalArgumentException("Could not read this Word file. Save it as .docx or paste the requirements.");
        }
    }

    private static String readDoc(byte[] bytes) {
        try (ByteArrayInputStream input = new ByteArrayInputStream(bytes);
                HWPFDocument document = new HWPFDocument(input);
                WordExtractor extractor = new WordExtractor(document)) {
            return extractor.getText();
        } catch (IOException | RuntimeException ex) {
            throw new IllegalArgumentException("Could not read this Word file. Save it as .docx or paste the requirements.");
        }
    }

    private static String readPlain(byte[] bytes) {
        byte[] payload = bytes;
        if (payload.length >= 3
                && payload[0] == (byte) 0xEF
                && payload[1] == (byte) 0xBB
                && payload[2] == (byte) 0xBF) {
            payload = java.util.Arrays.copyOfRange(payload, 3, payload.length);
        }
        if (looksBinary(payload)) {
            throw new IllegalArgumentException("This file is not plain text. Use PDF, Word, TXT, or Markdown.");
        }
        CharsetDecoder decoder = StandardCharsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT);
        try {
            return decoder.decode(ByteBuffer.wrap(payload)).toString();
        } catch (CharacterCodingException ex) {
            return new String(payload, Charset.forName("windows-1252"));
        }
    }

    private static boolean looksBinary(byte[] bytes) {
        int limit = Math.min(bytes.length, 4096);
        int suspicious = 0;
        for (int i = 0; i < limit; i++) {
            int value = bytes[i] & 0xFF;
            if (value == 0) {
                return true;
            }
            if (value < 0x09) {
                suspicious++;
            }
        }
        return limit > 0 && suspicious > limit / 10;
    }

    private static String normalize(String raw) {
        if (raw == null) {
            return "";
        }
        String text = raw.replace("\u0000", "")
                .replace("\r\n", "\n")
                .replace('\r', '\n')
                .replace('\f', '\n');
        text = text.replaceAll("[ \\t]{2,}", " ");
        text = text.replaceAll(" *\n", "\n");
        text = text.replaceAll("\n{3,}", "\n\n");
        return text.strip();
    }

    private static String extension(String fileName) {
        if (fileName == null) {
            return "";
        }
        String name = fileName.trim().toLowerCase(Locale.ROOT);
        int slash = Math.max(name.lastIndexOf('/'), name.lastIndexOf('\\'));
        if (slash >= 0) {
            name = name.substring(slash + 1);
        }
        int dot = name.lastIndexOf('.');
        if (dot < 0 || dot == name.length() - 1) {
            return "";
        }
        return name.substring(dot + 1);
    }

    private static String emptyMessage(String extension) {
        if ("pdf".equals(extension)) {
            return "This PDF has no selectable text. Paste the requirements instead.";
        }
        if ("doc".equals(extension) || "docx".equals(extension)) {
            return "This Word file has no readable text. Paste the requirements instead.";
        }
        return "The file has no readable text. Paste the requirements instead.";
    }
}
